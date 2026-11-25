#!/bin/sh
# Redis is a cache for the services; it lives and dies with the container.
# The platform's function executor runs Function and Skill code. mini's sandbox
# stand-in (port 8090) plays its control plane and receives its callbacks; code
# it runs reaches the platform through the gateway. An image built with
# MINI_SANDBOX=off has no executor: mini then runs no Function or Skill code.
# MINI_EXECUTOR=external: the executor runs in a container of its own, at
# MINI_EXECUTOR_URL, so that code sees none of mini's data, secrets or
# internal ports; mini starts no executor here.
set -e
mkdir -p /data/oss "$WORKSPACE_PATH"
redis-server --bind 127.0.0.1 --port 6379 --save "" --appendonly no \
  --daemonize yes --logfile /tmp/redis.log
gateway_port=${MINI_GATEWAY_ADDR##*:}
if [ "${MINI_EXECUTOR:-}" = external ]; then
  : # MINI_EXECUTOR_URL points at it
elif [ -x /app/.venv/bin/python ]; then
  (
    cd /app
    export CONTROL_PLANE_URL=http://127.0.0.1:8090 INTERNAL_API_TOKEN=mini \
      CONTAINER_ID=mini DISABLE_BWRAP=true EXECUTOR_HOST=127.0.0.1 \
      BKN_BASE_URL="http://127.0.0.1:$gateway_port" \
      BKN_SANDBOX_MCP_URL="http://127.0.0.1:$gateway_port/api/agent-retrieval/v1/mcp/"
    exec .venv/bin/python -m executor.interfaces.http.rest
  ) &
else
  unset MINI_EXECUTOR_URL
fi
exec /opt/mini/bin/mini
