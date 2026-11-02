# openbkn mini

[中文](README.zh.md)

OpenBKN for a developer's machine: modeling, data access (Vega), queries including Cypher, search
(Context Loader MCP) and the execution factory (API / MCP / Skill). Sign-in and permissions,
metrics, Trace, function execution and BKN Agent are not included.

The five platform Go services, stand-ins for the services mini does not run, the gateway, the
studio pages and Redis run in one container and one process. The other two containers are MariaDB
and OpenSearch.

## Start

```bash
docker compose up -d                 # the first run builds the image
open http://127.0.0.1:8000/studio/   # no sign-in; you are admin
```

Use `127.0.0.1`, not `localhost`: a studio dev server on port 8000 answers on `localhost`.

The build needs foundry and studio checkouts next to this repository, by default
`../bkn-foundry-wt-mini` and `../bkn-studio-wt-mini` (pinned commits of their `main`). Set
`MINI_FOUNDRY_DIR` and `MINI_STUDIO_SRC` to use others.

## Models (optional)

mini works without models; only semantic search is missing. To add them:

```bash
cp dev/run/models.example.json data/models.json   # pick your provider and models
echo 'DASHSCOPE_API_KEY=sk-...' > data/.env       # the variable named by api_key_env
docker compose restart mini
```

Each entry in `models.json` has `name`, `type` (embedding / reranker / llm), `url` (the full
endpoint), `api_key` or `api_key_env`, and `api_model` (the provider's name, defaults to `name`).
Embedding models also need `embedding_dim`; DashScope's native rerank API needs
`"format": "dashscope"`. With an embedding model configured, mini turns on embeddings for
knowledge network concepts.

## Layout

| Path | Contents |
|---|---|
| `cmd/mini` | Entry point: stand-ins, then the five services, then the gateway |
| `internal/gateway` | The single entry on port 8000: routes to the services, serves studio, writes config.js |
| `internal/fakes` | Stand-ins: bkn-safe, Hydra, model factory, Trace, sandbox, OSS, NSQ |
| `internal/forward` | In the container, relays the 127.0.0.1 database and OpenSearch ports to the compose services |
| `docker/` | Image, entry script and the migration tool's connection settings |
| `dev/` | For running mini directly on the host: a compose file for its dependencies and the run directory (configs) |
| `data/` | Your files: models.json, .env and uploaded files (not in git) |
