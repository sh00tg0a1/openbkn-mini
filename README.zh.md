# openbkn mini

[English](README.md)

开发者本机用的 OpenBKN：建模、数据接入（Vega）、查询（含 Cypher）、检索（Context Loader MCP）、
执行工厂（API / MCP / Skill）。不含登录与权限、指标、Trace、函数执行、BKN Agent。

平台的 5 个 Go 服务、mini 不运行的那些服务的替身、网关、studio 页面和 Redis 跑在一个容器、一个进程里；
另外两个容器是 MariaDB 和 OpenSearch。

## 启动

```bash
docker compose up -d                 # 第一次会构建镜像
open http://127.0.0.1:8000/studio/   # 免登录，以 admin 进入
```

地址要写 `127.0.0.1`，不要写 `localhost`：本机如果在 8000 端口开着 studio 开发服务器，`localhost` 会进到它那里。

构建需要旁边有 foundry 和 studio 的源码目录，默认 `../bkn-foundry-wt-mini`、`../bkn-studio-wt-mini`
（都是各自 `main` 的固定提交），可以用环境变量 `MINI_FOUNDRY_DIR`、`MINI_STUDIO_SRC` 换。

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
| `internal/fakes` | 替身：bkn-safe、Hydra、模型工厂、Trace、沙箱、OSS、NSQ |
| `internal/forward` | 容器里把 127.0.0.1 上的数据库、OpenSearch 端口转到 compose 服务 |
| `docker/` | 镜像、启动脚本、迁移工具的连接配置 |
| `dev/` | 在本机直接跑 mini 用：依赖服务的 compose 文件和运行目录（配置） |
| `data/` | 你自己的文件：models.json、.env、上传的文件（不进 git） |
