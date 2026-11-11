# openbkn mini

[English](README.md)

开发者本机用的 OpenBKN：建模、数据接入（Vega）、查询（含 Cypher）、检索（Context Loader MCP）、
行动、执行工厂（API / MCP / Skill / 函数）。不含指标、Trace、BKN Agent。默认免登录，所有人都是管理员；也可以打开平台自己的登录与权限（见下文）。

平台本身来自 [OpenBKN](https://github.com/openbkn-ai)：后端服务是
[bkn-foundry](https://github.com/openbkn-ai/bkn-foundry)，页面是
[bkn-studio](https://github.com/openbkn-ai/bkn-studio)。mini 用它们的源码构建，不改其中的代码。

一个容器里，平台的 5 个 Go 服务、mini 不运行的那些服务的替身、网关和 studio 页面跑在同一个进程里，
旁边是 Redis 和平台的函数执行器；另外两个容器是 MariaDB 和 OpenSearch。

函数和 Skill 脚本由平台自己的执行器运行，但没有社区版那样每个会话一个容器：它们共用 mini 容器里的
一个 Python 环境，和容器之间没有隔离。镜像以平台的沙箱模板镜像为底座，所以比较大（约 1.7 GB）。
用不到函数和 Skill 脚本的话，可以不装沙箱，见下文。

## 启动

```bash
docker compose up -d                 # 第一次会构建镜像
open http://127.0.0.1:8000/studio/   # 免登录，以 admin 进入
```

地址要写 `127.0.0.1`，不要写 `localhost`：本机如果在 8000 端口开着 studio 开发服务器，`localhost` 会进到它那里。

构建时按 `compose.yaml` 里固定的提交，从 GitHub 拉取 [bkn-foundry](https://github.com/openbkn-ai/bkn-foundry)
和 [bkn-studio](https://github.com/openbkn-ai/bkn-studio) 的源码。想用本地源码构建，设置环境变量
`MINI_FOUNDRY_DIR`、`MINI_STUDIO_SRC`。

## 换端口（可选）

```bash
echo 'MINI_PORT=8010' >> .env        # 和 compose.yaml 同目录的 .env，不是 data/.env
docker compose up -d
open http://127.0.0.1:8010/studio/
```

容器里也监听同一个端口，所以 mini 给出的链接（Skill 文件）在哪儿都能打开。容器里有几个端口已被占用
（包括 8080、9000），选到它们时 mini 启动就会报错，并说明被谁占用。

## 不装沙箱（可选）

```bash
echo 'MINI_SANDBOX=off' >> .env      # 和 compose.yaml 同目录的 .env，不是 data/.env
docker compose up -d --build
```

镜像不带函数执行器，小很多（约 310 MB）。函数和 Skill 脚本不运行（调试和调用都会失败），
页面上的函数入口和沙箱运行时管理也随之隐藏；其余功能不受影响。删掉这一行再 `up -d --build` 就装回来。

## 登录与权限（可选）

```bash
echo 'MINI_SAFE=on' >> .env                              # 和 compose.yaml 同目录的 .env
echo 'BKN_SAFE_INITIAL_PASSWORD=自己定一个' >> data/.env   # 管理员的初始密码
docker compose up -d
open http://127.0.0.1:8000/studio/                       # 用 admin 登录
```

这时 mini 在同一个进程里运行平台自己的 bkn-safe（用户、角色、权限），旁边运行 ORY Hydra（OAuth2），
不再用"所有人都是管理员"的替身。请求要带令牌，权限按真实规则判定。
不用重建镜像；改回 `MINI_SAFE=off` 再 `up -d` 即恢复免登录。

- 初始密码只在第一次创建管理员时读取。没有设置 `BKN_SAFE_INITIAL_PASSWORD` 时，随机密码只在日志里出现一次：
  `docker compose logs mini | grep initial_password`。
- 用启动时的地址打开 mini（`http://127.0.0.1:8000`，或 `MINI_PUBLIC_URL`）。登录绑定在这个地址上，
  换成 `localhost` 等别的地址登不上。
- openbkn 命令行的登录方式与平台相同（`openbkn auth login http://127.0.0.1:8000 -u admin -p ...`）。
  脚本里可以用 `scripts/login.py --password ...` 取令牌，作为 `BKN_TOKEN` 使用。
- 支持自助注册（OAuth 动态客户端注册）的 MCP 客户端，只填
  `http://127.0.0.1:8000/api/agent-retrieval/v1/mcp/` 这个地址即可连接，并在浏览器里完成登录。
- 用户和权限存在 MariaDB 的 `safe` 库里；Hydra 的客户端和会话存在 `data/hydra` 下的 SQLite 里。
  备份时 `data/hydra` 要和数据库卷一起保留。
- 建知识网络之前先定好用哪种方式。免登录时建的网络，打开登录后管理员能看到，但删不掉
  （bkn-safe 里没有它的代理账号），要关掉登录再管理。打开登录后建的网络，两种方式下都能用。
- 这是平台的登录，但走 http、只面向本机，不代表 mini 可以放到网络上。

## 配模型（可选）

不配模型也能用，只是没有语义检索。要配的话：

```bash
cp dev/run/models.example.json data/models.json   # 按需改服务商和模型
echo 'DASHSCOPE_API_KEY=sk-...' > data/.env       # models.json 里 api_key_env 指向的变量
docker compose restart mini
```

`models.json` 里每个模型写 `name`、`type`（embedding / reranker / llm）、`url`（完整接口地址）、
`api_key` 或 `api_key_env`、`api_model`（服务商侧的名字，默认同 `name`）。embedding 模型还要写
`embedding_dim`；百炼原生重排序接口加 `"format": "dashscope"`。配了 embedding 模型时，mini 会自动打开
知识网络概念的向量化。

## 目录

| 路径 | 内容 |
|---|---|
| `cmd/mini` | 入口：依次启动替身、5 个服务、网关 |
| `internal/safe` | `MINI_SAFE=on` 时用：bkn-safe 的配置与数据库、Hydra 进程、OAuth 客户端 |
| `internal/gateway` | 统一入口 8000：转发到各服务，托管 studio，生成 config.js |
| `internal/fakes` | 替身：bkn-safe、Hydra、模型工厂、Trace、沙箱控制面（后面接函数执行器）、OSS、NSQ |
| `internal/forward` | 容器里把 127.0.0.1 上的数据库、OpenSearch 端口转到 compose 服务 |
| `docker/` | 镜像、启动脚本、迁移工具的连接配置 |
| `dev/` | 在本机直接跑 mini 用：依赖服务的 compose 文件和运行目录（配置） |
| `scripts/smoke.sh` | 对运行中的 mini 做冒烟测试：数据源、建网、查询、Cypher、MCP、函数、Skill |
| `scripts/upstream.py` | 读取和更新固定的上游版本 |
| `data/` | 你自己的文件：models.json、.env、上传的文件（不进 git） |

## 版本与自动跟踪

`compose.yaml` 固定了 bkn-foundry、bkn-studio 的提交，以及由该 foundry 提交构建的平台镜像（迁移工具、
沙箱模板）。GitHub Actions 每晚（`.github/workflows/track-upstream.yml`）把它们更新到两个仓库 `main`
的最新提交，构建 mini，带沙箱和不带沙箱各跑一遍 `scripts/smoke.sh`（第二遍用 8010 端口），再打开登录跑一遍，通过后直接
提交到 `main`。如果 mini 改编过的 foundry 配置文件，或 `config.js` 依赖的 studio 代码有变化，提交说明里会
列出来。失败则开 issue。也可以手动触发，指定别的分支或提交。

自己跑冒烟测试：`scripts/smoke.sh`（需要 `openbkn` 命令行，跑完自动清理）。mini 打开了登录时要给出密码：
`MINI_PASSWORD=... scripts/smoke.sh`。

## 许可证

[OpenBKN License](LICENSE)，即改过的 Apache License 2.0。mini 构建和运行所用的平台源码与镜像，仍按
它们各自的许可证。
