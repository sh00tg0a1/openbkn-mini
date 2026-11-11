# openbkn mini

[中文](README.zh.md)

OpenBKN for a developer's machine: modeling, data access (Vega), queries including Cypher, search
(Context Loader MCP), actions, and the execution factory (API / MCP / Skill / Function). Metrics,
Trace and BKN Agent are not included. By default there is no sign-in and everyone is the
administrator; the platform's own sign-in and permissions can be turned on (below).

The platform itself is [OpenBKN](https://github.com/openbkn-ai): the backend services are
[bkn-foundry](https://github.com/openbkn-ai/bkn-foundry) and the web pages are
[bkn-studio](https://github.com/openbkn-ai/bkn-studio). mini builds them from source without
changing their code.

One container runs the five platform Go services, stand-ins for the services mini does not run,
the gateway and the studio pages in one process, next to Redis and the platform's function
executor. The other two containers are MariaDB and OpenSearch.

Functions and Skill scripts run in the platform's own executor, but without the community
edition's per-session containers: they share one Python environment in the mini container and are
not isolated from it. The image is built on the platform's sandbox template image, so it is large
(about 1.7 GB). If you do not need Functions or Skill scripts, leave the sandbox out (below).

## Start

```bash
docker compose up -d                 # the first run builds the image
open http://127.0.0.1:8000/studio/   # no sign-in; you are admin
```

Use `127.0.0.1`, not `localhost`: a studio dev server on port 8000 answers on `localhost`.

The build fetches [bkn-foundry](https://github.com/openbkn-ai/bkn-foundry) and
[bkn-studio](https://github.com/openbkn-ai/bkn-studio) from GitHub at the commits pinned in
`compose.yaml`. To build from local checkouts instead, set `MINI_FOUNDRY_DIR` and `MINI_STUDIO_SRC`.

## Another port (optional)

```bash
echo 'MINI_PORT=8010' >> .env        # the .env next to compose.yaml, not data/.env
docker compose up -d
open http://127.0.0.1:8010/studio/
```

mini listens on that port inside the container as well, so the links it hands out (Skill files)
work everywhere. A few ports are taken inside the container (8080 and 9000 among them); mini
refuses those at startup and says which part uses the port.

## Without the sandbox (optional)

```bash
echo 'MINI_SANDBOX=off' >> .env      # the .env next to compose.yaml, not data/.env
docker compose up -d --build
```

The image then has no function executor and is much smaller (about 310 MB). Functions and Skill
scripts do not run (debugging and calls fail), and studio hides the function entries and the
sandbox runtime page; everything else works as before. Remove the line and run
`up -d --build` again to bring the sandbox back.

## Sign-in and permissions (optional)

```bash
echo 'MINI_SAFE=on' >> .env                              # the .env next to compose.yaml
echo 'BKN_SAFE_INITIAL_PASSWORD=choose-one' >> data/.env # the administrator's first password
docker compose up -d
open http://127.0.0.1:8000/studio/                       # sign in as admin
```

mini then runs the platform's own bkn-safe (users, roles, permissions) in the same process and
ORY Hydra (OAuth2) next to it, instead of the stand-ins that let everyone in as the administrator.
Requests need a token and permissions are checked for real. No rebuild is needed; set `MINI_SAFE=off` and `up -d` to go back.

- The password is read only when the administrator is first created. Without
  `BKN_SAFE_INITIAL_PASSWORD` a random one is written to the log once:
  `docker compose logs mini | grep initial_password`.
- Open mini at the address it was started with (`http://127.0.0.1:8000`, or `MINI_PUBLIC_URL`).
  Sign-in is bound to that address and does not work through another one such as `localhost`.
- The openbkn CLI signs in as on the platform (`openbkn auth login http://127.0.0.1:8000 -u admin
  -p ...`). For scripts, `scripts/login.py --password ...` prints a token to use as `BKN_TOKEN`.
- MCP clients that register themselves (OAuth dynamic client registration) can connect to
  `http://127.0.0.1:8000/api/agent-retrieval/v1/mcp/` with the address alone and sign the user in
  in the browser.
- Users and permissions are kept in a `safe` database in MariaDB; Hydra keeps its clients and
  sessions in SQLite under `data/hydra`. Keep `data/hydra` together with the database volume.
- Choose before you build networks. A network created without sign-in shows up for the
  administrator afterwards but cannot be deleted while sign-in is on (bkn-safe has no proxy
  account for it); turn sign-in off to manage it. Networks created with sign-in work either way.
- This is the platform's sign-in served over http for one machine. It does not make mini safe to
  put on a network.

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
| `internal/safe` | For `MINI_SAFE=on`: bkn-safe's settings and database, the Hydra process, the OAuth clients |
| `internal/gateway` | The single entry on port 8000: routes to the services, serves studio, writes config.js |
| `internal/fakes` | Stand-ins: bkn-safe, Hydra, model factory, Trace, the sandbox control plane (in front of the function executor), OSS, NSQ |
| `internal/forward` | In the container, relays the 127.0.0.1 database and OpenSearch ports to the compose services |
| `docker/` | Image, entry script and the migration tool's connection settings |
| `dev/` | For running mini directly on the host: a compose file for its dependencies and the run directory (configs) |
| `scripts/smoke.sh` | Smoke test against a running mini: data source, network, query, Cypher, MCP, Function, Skill |
| `scripts/upstream.py` | The pinned upstream versions, and moving them forward |
| `data/` | Your files: models.json, .env and uploaded files (not in git) |

## Versions and tracking

`compose.yaml` pins the bkn-foundry and bkn-studio commits and the platform images built from that
foundry commit (the migration tool and the sandbox template). Every night a GitHub Actions workflow
(`.github/workflows/track-upstream.yml`) moves the pins to the newest `main` of both, builds mini,
runs `scripts/smoke.sh` with and without the sandbox (the second run on port 8010) and once more
with sign-in turned on, and commits the new pins to `main`. The commit message lists the foundry config files mini keeps adapted copies of
and the studio code its `config.js` depends on, when those changed. A failure opens an issue
instead. You can also start the workflow by hand, with other branches or commits.

To run the smoke test yourself: `scripts/smoke.sh` (it needs the `openbkn` CLI and cleans up after
itself). Against a mini with sign-in, give it the password: `MINI_PASSWORD=... scripts/smoke.sh`.

## License

[OpenBKN License](LICENSE), a modified Apache License 2.0. The platform sources and images mini
builds from and runs keep their own licenses.
