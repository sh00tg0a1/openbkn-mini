#!/usr/bin/env python3
"""The upstream versions openbkn mini pins, and moving them forward.

  upstream.py pins
      Print the pinned versions as KEY=VALUE lines.
  upstream.py bump FOUNDRY_COMMIT STUDIO_COMMIT [--report FILE]
      Pin these commits and the platform images built from (or before) the
      foundry commit, sync the files mini copies verbatim from foundry, run
      `go mod tidy`, and write a Markdown summary of the change to FILE.

bump reads bkn-foundry and bkn-studio checkouts with history, by default the
directories go.mod and compose.yaml use: ../bkn-foundry-wt-mini (checked out at
FOUNDRY_COMMIT, since go.mod builds against it) and ../bkn-studio-wt-mini.
Override with FOUNDRY_DIR / STUDIO_DIR.
"""
import json
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
COMPOSE = os.path.join(ROOT, "compose.yaml")
DOCKERFILE = os.path.join(ROOT, "docker", "Dockerfile")
FOUNDRY_URL = "https://github.com/openbkn-ai/bkn-foundry"
STUDIO_URL = "https://github.com/openbkn-ai/bkn-studio"

# Platform images built from bkn-foundry that mini pins (GHCR, public).
IMAGES = {
    "MIGRATOR_IMAGE": "core-data-migrator",
    "SANDBOX_IMAGE": "sandbox-template-python-basic",
}

# Files mini copies from foundry unchanged: bump syncs them.
VERBATIM = {
    "dev/run/authorization-registry.json": "bkn-safe/server/internal/seed/data/authorization-registry.json",
}

# Files mini adapted from foundry: bump only reports when the original changed.
ADAPTED = {
    "dev/run/config/bkn-backend-config.yaml": "adp/bkn/bkn-backend/server/config/bkn-backend-config.yaml",
    "dev/run/config/ontology-query-config.yaml": "adp/bkn/ontology-query/server/config/ontology-query-config.yaml",
    "dev/run/config/vega-backend-config.yaml": "vega/vega-backend/server/config/vega-backend-config.yaml",
    "dev/run/profile/agent-operator-integration.yaml":
        "adp/execution-factory/operator-integration/server/infra/config/agent-operator-integration.yaml",
    "dev/run/profile/agent-operator-integration-secret.yaml":
        "adp/execution-factory/operator-integration/server/infra/config/agent-operator-integration-secret.yaml",
    "dev/run/profile/mq_config.yaml": "adp/execution-factory/operator-integration/server/infra/config/mq_config.yaml",
    "dev/run/profile/agent-retrieval.yaml": "adp/context-loader/agent-retrieval/server/infra/config/agent-retrieval.yaml",
    "dev/run/profile/agent-retrieval-secret.yaml":
        "adp/context-loader/agent-retrieval/server/infra/config/agent-retrieval-secret.yaml",
    "dev/run/profile/observability.yaml": "adp/context-loader/agent-retrieval/server/infra/config/observability.yaml",
}

# Studio code the gateway's config.js depends on (navigation keys, permission
# names, runtime config): bump reports changes to it.
STUDIO_WATCH = [
    "src/framework/runtime",
    "src/app/shell",
    "src/modules/*/navigation.tsx",
    "src/modules/*/permissions.ts",
    "src/modules/knowledge-network/scenes/KnowledgeNetworkWorkspaceScene.tsx",
]

PIN_PATTERNS = {
    "FOUNDRY": r"bkn-foundry\.git#([0-9a-f]{40})",
    "STUDIO": r"bkn-studio\.git#([0-9a-f]{40})",
    "MIGRATOR_IMAGE": r"ghcr\.io/openbkn-ai/core-data-migrator:(\S+)",
    "SANDBOX_IMAGE": r"ghcr\.io/openbkn-ai/sandbox-template-python-basic:(\S+)",
}

MAIN_TAG = re.compile(r"^\d+\.\d+\.\d+-main\.(\d{14})\.sha([0-9a-f]{7,40})$")


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def write(path, text):
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def git(repo, *args, check=True):
    return subprocess.run(["git", "-C", repo] + list(args), check=check,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)


def pins():
    text = read(COMPOSE)
    out = {}
    for key, pattern in PIN_PATTERNS.items():
        m = re.search(pattern, text)
        if not m:
            sys.exit("compose.yaml has no pin for " + key)
        out[key] = m.group(1)
    return out


def curl_json(url, token=None):
    """GET url with curl (system CA roots); return (headers, JSON body)."""
    cmd = ["curl", "-fsS", "--max-time", "30", "-D", "-", url]
    if token:
        cmd += ["-H", "Authorization: Bearer " + token]
    out = subprocess.run(cmd, check=True, stdout=subprocess.PIPE, universal_newlines=True).stdout
    # universal_newlines turns \r\n into \n; a proxy adds its own header block.
    head, _, body = out.partition("\n\n")
    while body.startswith("HTTP/"):
        head, _, body = body.partition("\n\n")
    return head, json.loads(body)


def ghcr_tags(image):
    """All tags of ghcr.io/openbkn-ai/<image>, read anonymously."""
    _, auth = curl_json("https://ghcr.io/token?scope=repository:openbkn-ai/%s:pull" % image)
    tags, url = [], "https://ghcr.io/v2/openbkn-ai/%s/tags/list?n=1000" % image
    while url:
        head, page = curl_json(url, auth["token"])
        tags += page.get("tags") or []
        m = re.search(r'(?im)^link:\s*<([^>]+)>;\s*rel="next"', head)
        url = "https://ghcr.io" + m.group(1) if m else None
    return tags


def image_tag_for(image, foundry, commit):
    """The newest main-branch tag of image built from commit or an ancestor."""
    candidates = []
    for tag in ghcr_tags(image):
        m = MAIN_TAG.match(tag)
        if m:
            candidates.append((m.group(1), m.group(2), tag))
    for _, sha, tag in sorted(candidates, reverse=True):
        resolved = git(foundry, "rev-parse", "--verify", "--quiet", sha + "^{commit}", check=False)
        if resolved.returncode != 0:
            continue
        if git(foundry, "merge-base", "--is-ancestor", resolved.stdout.strip(), commit, check=False).returncode == 0:
            return tag
    sys.exit("no %s image built from %s or before it" % (image, commit))


def changed(repo, old, new, paths):
    """`git diff --stat` lines for paths changed between old and new."""
    out = git(repo, "diff", "--stat=200", old, new, "--", *paths).stdout.strip().splitlines()
    return out[:-1]  # drop the summary line


def bump(foundry_commit, studio_commit, report_path):
    foundry = os.environ.get("FOUNDRY_DIR") or os.path.join(ROOT, "..", "bkn-foundry-wt-mini")
    studio = os.environ.get("STUDIO_DIR") or os.path.join(ROOT, "..", "bkn-studio-wt-mini")
    foundry_commit = git(foundry, "rev-parse", foundry_commit + "^{commit}").stdout.strip()
    studio_commit = git(studio, "rev-parse", studio_commit + "^{commit}").stdout.strip()
    head = git(foundry, "rev-parse", "HEAD").stdout.strip()
    if head != foundry_commit:
        sys.exit("%s is at %s, not %s: go.mod builds against it" % (foundry, head[:9], foundry_commit[:9]))

    old = pins()
    new = {"FOUNDRY": foundry_commit, "STUDIO": studio_commit}
    for key, image in IMAGES.items():
        new[key] = image_tag_for(image, foundry, foundry_commit)

    for path in (COMPOSE, DOCKERFILE):
        text = read(path)
        for key in new:
            text = text.replace(old[key], new[key])
        write(path, text)

    synced = []
    for mine, theirs in VERBATIM.items():
        src = read(os.path.join(foundry, theirs))
        if read(os.path.join(ROOT, mine)) != src:
            write(os.path.join(ROOT, mine), src)
            synced.append(mine)

    subprocess.run(["go", "mod", "tidy"], cwd=ROOT, check=True)

    lines = ["Moves openbkn mini to newer upstream versions. The build and the smoke test "
             "(`scripts/smoke.sh`, with and without the sandbox) passed on them.", ""]
    lines += ["| | from | to |", "|---|---|---|"]
    for key, url in (("FOUNDRY", FOUNDRY_URL), ("STUDIO", STUDIO_URL)):
        repo = foundry if key == "FOUNDRY" else studio
        if old[key] == new[key]:
            lines.append("| %s | `%s` | unchanged |" % (key.lower(), old[key][:9]))
            continue
        count = git(repo, "rev-list", "--count", "%s..%s" % (old[key], new[key])).stdout.strip()
        lines.append("| %s | `%s` | `%s` ([%s commits](%s/compare/%s...%s)) |" % (
            key.lower(), old[key][:9], new[key][:9], count, url, old[key][:12], new[key][:12]))
    for key, image in IMAGES.items():
        lines.append("| %s | `%s` | %s |" % (
            image, old[key], "unchanged" if old[key] == new[key] else "`%s`" % new[key]))

    adapted = changed(foundry, old["FOUNDRY"], new["FOUNDRY"], list(ADAPTED.values()))
    studio_changes = changed(studio, old["STUDIO"], new["STUDIO"], STUDIO_WATCH)
    lines += ["", "**Needs a look**", ""]
    if adapted:
        lines += ["Foundry changed config files mini keeps adapted copies of (under `dev/run/`); "
                  "carry the changes over if they matter:", "", "```"] + adapted + ["```", ""]
    if studio_changes:
        lines += ["Studio changed code that the gateway's `config.js` depends on (navigation keys, "
                  "permissions); check the hidden and shown entries:", "", "```"] + studio_changes + ["```", ""]
    if not adapted and not studio_changes:
        lines += ["Nothing: the adapted config files and the studio code config.js depends on are unchanged.", ""]
    if synced:
        lines += ["Synced unchanged copies: " + ", ".join("`%s`" % p for p in synced), ""]

    report = "\n".join(lines) + "\n"
    if report_path:
        write(report_path, report)
    else:
        sys.stdout.write(report)


def main(argv):
    if len(argv) >= 1 and argv[0] == "pins":
        for key, value in pins().items():
            print("%s=%s" % (key, value))
        return
    if len(argv) >= 3 and argv[0] == "bump":
        report = None
        if len(argv) == 5 and argv[3] == "--report":
            report = argv[4]
        elif len(argv) != 3:
            sys.exit(__doc__)
        bump(argv[1], argv[2], report)
        return
    sys.exit(__doc__)


if __name__ == "__main__":
    main(sys.argv[1:])
