# openbkn mini

[English](README.md)

开发者本机用的 OpenBKN：建模、数据接入（Vega）、查询（含 Cypher）、检索（Context Loader MCP）、
行动、执行工厂（API / MCP / Skill / 函数）。不含登录与权限、指标、Trace、BKN Agent。

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

构建需要旁边有 foundry 和 studio 的源码目录，默认 `../bkn-foundry-wt-mini`、`../bkn-studio-wt-mini`
（都是各自 `main` 的固定提交），可以用环境变量 `MINI_FOUNDRY_DIR`、`MINI_STUDIO_SRC` 换。

## 不装沙箱（可选）

```bash
echo 'MINI_SANDBOX=off' >> .env      # 和 compose.yaml 同目录的 .env，不是 data/.env
docker compose up -d --build
```

镜像不带函数执行器，小很多（约 310 MB）。函数和 Skill 脚本不运行（调试和调用都会失败），
页面上的函数入口和沙箱运行时管理也随之隐藏；其余功能不受影响。删掉这一行再 `up -d --build` 就装回来。

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
| `internal/gateway` | 统一入口 8000：转发到各服务，托管 studio，生成 config.js |
| `internal/fakes` | 替身：bkn-safe、Hydra、模型工厂、Trace、沙箱控制面（后面接函数执行器）、OSS、NSQ |
| `internal/forward` | 容器里把 127.0.0.1 上的数据库、OpenSearch 端口转到 compose 服务 |
| `docker/` | 镜像、启动脚本、迁移工具的连接配置 |
| `dev/` | 在本机直接跑 mini 用：依赖服务的 compose 文件和运行目录（配置） |
| `data/` | 你自己的文件：models.json、.env、上传的文件（不进 git） |
