#!/usr/bin/env bash
# Smoke test for a running openbkn mini started with docker compose: a data
# source, a network built from it, instance query, Cypher, MCP, and Function
# and Skill code (or, without the sandbox, their clear refusal).
#
# Usage: scripts/smoke.sh [docker compose options, e.g. -p <project>]
# MINI_URL is the gateway (default http://127.0.0.1:8000). Needs docker
# compose, the openbkn CLI, curl and python3. It removes what it creates.
set -euo pipefail
cd "$(dirname "$0")/.."

URL=${MINI_URL:-http://127.0.0.1:8000}
COMPOSE=(docker compose "$@")
export BKN_BASE_URL=$URL BKN_TOKEN=mini BKN_SKIP_VERSION_CHECK=1
TAG=mini-smoke-$(date +%s)
WORK=$(mktemp -d)
LOG=$WORK/cli.log
: >"$LOG"

step() { printf '== %s\n' "$*"; }
fail() {
  printf 'FAIL: %s\n' "$*" >&2
  if [ -s "$LOG" ]; then tail -20 "$LOG" >&2; fi
  exit 1
}
# json EXPR: print a Python expression over the JSON document on stdin (d).
json() { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }
cli() { openbkn --json "$@" 2>>"$LOG"; }
# retry CMD...: up to 15 tries, 2 s apart. A new network or relation answers
# 503 SyncPending until its proxy grants have synced in the background.
retry() {
  for _ in $(seq 15); do
    "$@" 2>>"$LOG" && return 0
    sleep 2
  done
  return 1
}
# entries: the list part of a CLI list answer.
ENTRIES="(d.get('entries') or d.get('data') or (d if isinstance(d, list) else []))"

KN='' CATALOG='' BOX='' SKILL=''
cleanup() {
  set +e
  if [ -n "$SKILL" ]; then
    cli skill set-status "$SKILL" offline >/dev/null
    cli skill delete "$SKILL" >/dev/null # 500 without an embedding model, still deleted
  fi
  [ -n "$BOX" ] && openbkn toolbox delete "$BOX" -y >/dev/null 2>&1
  [ -n "$KN" ] && openbkn bkn delete "$KN" -y >/dev/null 2>&1
  [ -n "$CATALOG" ] && openbkn vega catalog delete "$CATALOG" >/dev/null 2>&1
  "${COMPOSE[@]}" exec -T mariadb mariadb -uroot -pmini \
    -e "DROP DATABASE IF EXISTS mini_smoke; DROP USER IF EXISTS 'mini_smoke'@'%';" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

step "studio"
for _ in $(seq 60); do
  curl -fs -o /dev/null "$URL/studio/" && break
  sleep 2
done
curl -fs -o /dev/null "$URL/studio/" || fail "studio is not served at $URL"
if curl -fs "$URL/studio/config.js" | grep -q 'execution-factory:function:create'; then
  SANDBOX=on
else
  SANDBOX=off
fi
echo "   sandbox: $SANDBOX"

step "data source"
"${COMPOSE[@]}" exec -T mariadb mariadb -uroot -pmini <test/smoke/data.sql || fail "load test data"
CATALOG=$(cli vega catalog create --name "$TAG" --connector-type mysql --enabled \
  --connector-config '{"host":"mariadb","port":3306,"username":"mini_smoke","password":"mini_smoke","database":"mini_smoke"}' |
  json "d['id']") || fail "create catalog"
cli vega catalog discover "$CATALOG" >/dev/null || fail "discover tables"
tables=0
for _ in $(seq 30); do
  tables=$(cli vega catalog resources "$CATALOG" | json "len($ENTRIES)") || tables=0
  [ "$tables" -ge 2 ] && break
  sleep 2
done
[ "$tables" -ge 2 ] || fail "discovered $tables tables, want 2"

step "network from the catalog"
KN=$(cli bkn create-from-catalog "$CATALOG" --name "$TAG" | json "d['kn_id']") || fail "create-from-catalog"
OTS=$(cli bkn object-type list "$KN") || fail "list object types"
PART=$(json "next(x['id'] for x in $ENTRIES if x['name'] == 'part')" <<<"$OTS") || fail "no part object type"
SUPPLIER=$(json "next(x['id'] for x in $ENTRIES if x['name'] == 'supplier')" <<<"$OTS") || fail "no supplier object type"

step "instance query"
suppliers() { cli bkn object-type query "$KN" "$SUPPLIER" --body '{"limit":10}' | json "len(d['datas'])"; }
rows=$(retry suppliers) || fail "query suppliers"
[ "$rows" = 2 ] || fail "$rows suppliers, want 2"

step "relation and Cypher"
cli bkn relation-type create "$KN" --body "{\"entries\":[{\"id\":\"part_supplier\",\"name\":\"part_supplier\",
  \"source_object_type_id\":\"$PART\",\"target_object_type_id\":\"$SUPPLIER\",\"type\":\"direct\",
  \"mapping_rules\":[{\"source_property\":{\"name\":\"supplier_id\"},\"target_property\":{\"name\":\"id\"}}]}]}" \
  >/dev/null || fail "create relation"
CYPHER="MATCH (p:part)-[:part_supplier]->(s:supplier) WHERE s.city = '上海' RETURN p.name AS part"
cypher_parts() { cli bkn cypher "$KN" --query "$CYPHER" | json "','.join(sorted(e['part'] for e in d['entries']))"; }
parts=$(retry cypher_parts) || fail "Cypher"
[ "$parts" = "轴承,齿轮" ] || fail "Cypher answered '$parts', want 轴承,齿轮"

step "MCP"
MCP="$URL/api/agent-retrieval/v1/mcp/"
H=(-H 'Authorization: Bearer mini' -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream')
SID=$(curl -fs -D - -o /dev/null -X POST "$MCP" "${H[@]}" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mini-smoke","version":"1"}}}' |
  tr -d '\r' | awk 'tolower($1) == "mcp-session-id:" {print $2}')
[ -n "$SID" ] || fail "no MCP session"
H+=(-H "Mcp-Session-Id: $SID")
curl -fs -o /dev/null -X POST "$MCP" "${H[@]}" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' || fail "MCP initialized"
# mcp TOOL ARGS: call a tool and print its result (structured, or its text).
mcp() {
  curl -fs -X POST "$MCP" "${H[@]}" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}" |
    sed 's/^data: //' | grep '^{' | python3 -c '
import sys, json
r = json.load(sys.stdin)["result"]
if r.get("isError"):
    sys.exit("tool error: " + json.dumps(r, ensure_ascii=False)[:500])
sc = r.get("structuredContent")
print(json.dumps(sc, ensure_ascii=False) if sc else r["content"][0]["text"])'
}
CTX=$(mcp bkn_start_interaction '{"agent_name":"mini-smoke","conversation_mode":"new","question":"上海供应商供哪些零件？"}' |
  json "json.dumps({'conversation_id': d['conversation_id'], 'interaction_id': d['interaction_id']})") ||
  fail "start interaction"
ANSWER=$(retry mcp run_cypher "{\"kn_id\":\"$KN\",\"query\":\"$CYPHER\",\"bkn_context\":$CTX}") ||
  fail "run_cypher over MCP"
grep -q '轴承' <<<"$ANSWER" || fail "run_cypher over MCP answered: ${ANSWER:0:300}"
IID=$(json "d['interaction_id']" <<<"$CTX")
mcp bkn_finish_interaction "{\"interaction_id\":\"$IID\",\"outcome\":\"completed\",\"answer\":\"轴承、齿轮\"}" >/dev/null ||
  fail "finish interaction"

step "Function"
BOX=$(cli toolbox create --name "${TAG//-/_}" --type function --description "mini smoke test" |
  json "d.get('box_id') or d.get('id')") || fail "create toolbox"
TOOL=$(cli function create test/smoke/echo_fn.py --toolbox "$BOX" --name echo_products \
  --description "echo the products it receives" \
  --inputs '[{"name":"products","type":"array","required":false,"description":"product codes"}]' |
  json "d.get('tool_id') or (d.get('success_ids') or [''])[0]") || fail "create function"
RESULT=$(cli function debug "$TOOL" --toolbox "$BOX" --body '{"products":["A","B","C"]}') || fail "debug function"
if [ "$SANDBOX" = on ]; then
  count=$(json "(d['body'].get('result') or {}).get('count')" <<<"$RESULT")
  [ "$count" = 3 ] || fail "function returned count=$count: ${RESULT:0:400}"
else
  grep -q 'without a function executor' <<<"$RESULT" || fail "function without executor: ${RESULT:0:400}"
fi

step "Skill script"
mkdir -p "$WORK/skill"
cp -R test/smoke/skill/mini-smoke "$WORK/skill/$TAG"
sed -i.bak "s/^name: mini-smoke$/name: $TAG/" "$WORK/skill/$TAG/SKILL.md" && rm "$WORK/skill/$TAG/SKILL.md.bak"
SKILL=$(cli skill register "$WORK/skill/$TAG" | json "d.get('skill_id') or d.get('id')") || fail "register skill"
cli skill set-status "$SKILL" published >/dev/null || fail "publish skill"
RESULT=$(curl -fs -X POST "$URL/api/agent-operator-integration/v1/skills/$SKILL/execute" \
  -H 'Authorization: Bearer mini' -H 'Content-Type: application/json' \
  -d '{"entry_shell":"sh scripts/hello.sh","timeout":60}') || fail "execute skill"
if [ "$SANDBOX" = on ]; then
  grep -q 'pandas' <<<"$RESULT" || fail "skill script: ${RESULT:0:400}"
else
  grep -q 'without a function executor' <<<"$RESULT" || fail "skill without executor: ${RESULT:0:400}"
fi

echo "smoke test passed (sandbox $SANDBOX)"
