# openbkn mini 设计

## 1. 目标

openbkn mini 是给开发者在自己机器上使用的 OpenBKN。它包含平台的核心功能，去掉认证与权限，用一个脚本在 Docker 上启动，不需要 Kubernetes。

开发者用它来：

- 用 BKN 建模，由 Agent（Claude Code、Codex 等）编写 `.bkn` 文件并推送；
- 接入数据，查询实例、沿关系遍历、执行 Cypher；
- 做全文与语义检索，让 Agent 通过 Context Loader（MCP）使用知识网络；
- 定义与执行行动、工具和函数；
- 与 BKN Agent 对话；
- 在 studio 中查看模型、实例和检索结果。

### 1.1 范围

| 包含 | 不包含 |
|---|---|
| 建模：网络、对象类、关系类、行动类、概念组、值类型、逻辑属性 | 认证、用户、权限管理 |
| 数据接入（Vega）：连接外部数据库、导入文件、批量构建索引 | Vega 流式构建 |
| 查询：实例查询、关系遍历、Cypher | 指标与指标分析 |
| 检索：全文、语义、Context Loader（MCP） | BKN Trace（只保留让其他功能正常工作的替身） |
| 行动执行、执行工厂（工具、函数、MCP、Skill） | 模型工厂（由配置文件替代） |
| BKN Agent | 多人共用、生产部署、运行不可信代码 |

### 1.2 原则

1. **平台代码零改动。** mini 使用与平台同一版本的官方程序，只通过配置和替身服务适配。平台的行为在 mini 中保持一致。
2. **替身只做让平台正常工作的最小实现。** 替身不存业务数据，不做真实的鉴权与采集。
3. **装和跑要简单。** 一个脚本启动，一份模型配置文件，数据放在 Docker 卷中。

## 2. 总体架构

mini 由 3 个容器组成，用 docker compose 编排：

| 容器 | 镜像 | 内容 |
|---|---|---|
| `mini` | openbkn-mini 构建 | 平台服务、mini-core（网关、进程看护、替身）、函数执行器、Redis、studio 页面 |
| `mariadb` | MariaDB 官方镜像 | 平台元数据库 `openbkn` |
| `opensearch` | OpenSearch 官方镜像 | 检索与向量索引、Vega 导入的数据集 |

另有一个一次性容器 `migrator`，使用平台官方的 data-migrator 镜像，在 `mini` 启动前建表或执行增量迁移。

### 2.1 mini 容器内的进程

| 进程 | 来源 | 语言 |
|---|---|---|
| bkn-backend | 平台官方镜像 | Go |
| ontology-query | 平台官方镜像 | Go |
| vega-backend | 平台官方镜像 | Go |
| 执行工厂（operator-integration） | 平台官方镜像 | Go |
| Context Loader（agent-retrieval） | 平台官方镜像 | Go |
| BKN Agent | 平台官方镜像 | Python |
| mini-core | 新写 | Go |
| 函数执行器 | 新写，复用沙箱 SDK | Python |
| Redis | 发行版软件包 | — |

mini-core 负责三件事：

1. **进程看护**：按顺序拉起其他进程，异常退出时重启，统一输出日志。
2. **网关**：对外开放一个端口（默认映射到本机 8000）。`/api/...` 按平台网关的路径表转发；其余请求返回 studio 页面。
3. **替身服务**：Hydra、bkn-safe、版本授权、模型、Trace、消息，见第 4 节。

### 2.2 服务之间的寻址

平台服务按 Kubernetes 服务名寻址（如 `bkn-safe:3000`、`mf-model-api:9898`、`agent-operator-integration:9000`）。mini 容器在 `/etc/hosts` 中把这些名字都指向 `127.0.0.1`，替身在原端口上监听。部分名字写死在代码中（如执行工厂的 `agent-operator-integration:9000`），必须保留原名与原端口。

已知的地址：

| 名字 | 端口 | 由谁提供 |
|---|---|---|
| `bkn-safe` | 3000 | mini-core |
| `bkn-safe-hydra-admin` | 4445 | mini-core |
| `mf-model-api`、`mf-model-manager`（含 `.openbkn` 后缀形式） | 9898 | mini-core（按路径前缀区分） |
| `sandbox-control-plane` | 8000 | 函数执行器 |
| `agent-observability-internal` | 8081 | mini-core（Trace 替身） |
| `agent-operator-integration` | 9000 | 执行工厂 |
| `agent-retrieval` | 30779 | Context Loader |
| `bkn-agent` | 30800 | BKN Agent |
| `mariadb`、`opensearch` | 3306、9200 | 对应容器 |

其余服务的端口在实现时逐个核对，避免冲突。

## 3. 复用与版本

- **程序来源**：`mini` 镜像用多阶段构建，从同版本的平台官方镜像中拷出各服务的可执行文件、静态资源和 studio 页面，不从源码编译。所谓"pin 平台版本"，就是固定这些官方镜像的版本号。
- **foundry 子模块**：openbkn-mini 以子模块引入 bkn-foundry，只用于读取种子数据（如权限注册表 `bkn-safe/server/internal/seed/data/authorization-registry.json`）、接口定义（`docs/api/`）和契约测试样例，不参与构建。
- **版本号**：mini 的版本号与平台一致，例如 mini 0.2.1 使用平台 0.2.1 的全部镜像。平台发布后，mini 跑通第 9 节的测试再发布。
- **更新版本**：改版本号、对照平台各服务 Helm 模板的变化同步 mini 的配置、跑测试。

## 4. 替身服务

所有替身都把调用者当作平台内置的超级管理员：

- 用户 ID：`266c6a42-6131-4d62-8f39-853e7093701c`
- 角色：`super_admin`（`7dcfcc9c-ad02-11e8-aa06-000c29358ad6`）

平台各服务在代码中写死了这个 ID 作为管理员。mini 使用同一个 ID，资源的创建者与平台初始管理员一致，为以后整库迁移到社区版留下余地。

### 4.1 Hydra

| 接口 | 行为 |
|---|---|
| `POST /admin/oauth2/introspect` | 任何令牌（包括空令牌）都返回有效的超级管理员 |

返回体照 `bkn-safe/contract/testdata/introspect/user.json` 的格式：`active`、`sub`（超级管理员 ID）、`scope`、`client_id`（与 `sub` 不同）、`ext` 中的 `visitor_type`（`realname`）、`login_ip`、`udid`、`account_type`、`client_type` 五个字符串缺一不可。平台公共库读取这些字段时不做类型检查，缺字段会使服务崩溃。

studio 以嵌入模式运行（`window.__BKN_STUDIO_RUNTIME__ = { mode: "hosted" }`），不走 OAuth 登录；`openbkn` 命令行用 `BKN_TOKEN` 直接传令牌。因此 OAuth 授权、令牌、登出等接口都不需要。

### 4.2 bkn-safe

服务间调用（不带令牌，`accessor_id` 在请求体中）：

| 接口 | 返回 |
|---|---|
| `POST /api/safe/v1/authz/checks` | 每项 `allowed: true`；`results` 与请求顺序一致、逐项回显；顶层 `allowed: true` |
| `POST /api/safe/v1/authz/resource-filter` | 回显全部请求的资源；`include_operations=true` 时按权限注册表给出该类型的全部操作 |
| `GET /api/safe/v1/authz/resources` | `{"ids":[],"unrestricted":true,"requires_candidate_filter":false}` |
| `POST /api/safe/v1/authz/row-filters` | 每个对象类一条，`predicate` 为 `{"kind":"true"}`；`entries` 不能为空值 |
| `POST /api/safe/v1/authz/property-levels` | 每个属性 `level: "full"` |
| `POST`、`DELETE /api/safe/v1/authz/policies` | 204 |
| `PUT`、`DELETE`、`GET /api/safe/v1/authz/resource-parents` | 写操作返回成功；查询返回 `{"items":[],"total":0}` |
| `POST /api/safe/v1/api-keys/introspect` | `{"active": false}`（mini 不签发 AppKey） |
| `POST /api/safe/v1/directory/names` | 按 ID 返回名称，超级管理员返回固定名称，其余返回 ID 本身 |
| `POST /api/safe/v1/directory/users-detail` | 超级管理员的用户信息 |
| `GET /api/safe/v1/directory/users/{id}` | 同上 |
| `GET /api/safe/v1/internal/license/current` | 404，各服务据此按社区版运行 |
| `POST /api/safe/in/v1/managed-proxy-accounts` | 见下文"代理账号" |
| `GET /api/safe/in/v1/managed-proxy-accounts/{id}` | 同上 |
| `POST /api/safe/in/v1/managed-proxy-accounts/{id}/{disable,restore,archive}` | 同上 |
| `POST /api/safe/in/v1/proxy-grant-sources/check` | `{"allowed": true}` |
| `POST /api/safe/in/v1/proxy-grant-sources/{check-batch,check-delta}` | `denied_sources` 为空，`resolved_sources` 回显请求并补 `granted_by` |
| `POST /api/safe/in/v1/proxy-grant-sources/{sync,sync-delta}` | 计数全为 0 |
| `POST /api/safe/in/v1/proxy-grant-sources/reconcile` | 计数全为 0 |

**代理账号**：发布知识网络时 bkn-backend 会创建代理账号，并严格校验返回值。替身不存状态：账号 ID 由知识网络 ID 推算；创建与查询都现场生成一个 `managed_by: "bkn"`、`account_type: "app"`、启用中、不可登录、不可签发凭证的账号。

studio 调用（带令牌）：

| 接口 | 返回 |
|---|---|
| `GET /api/safe/v1/capabilities` | 社区版、未授权 |
| `GET /api/safe/v1/me` | 超级管理员，`roles` 含 `super_admin` |
| `GET /api/safe/v1/me/permissions` | `is_admin: true`，权限为 `*:*` |
| `GET /api/safe/v1/me/authorization-registry` | 权限注册表种子文件 |
| `POST /api/safe/v1/me/logout` | 204 |

studio 的系统管理（用户、角色、授权）页面在 mini 中隐藏，相关接口不实现。

### 4.3 模型

mini-core 读取 `models.yaml`，实现平台模型服务的接口，把请求转发到 OpenAI 兼容地址：

```yaml
models:
  - id: llm-default            # 固定 ID，平台数据中按此引用
    type: llm
    default: true
    upstream: { base_url: https://api.example.com/v1, model: some-chat-model, api_key_env: LLM_API_KEY }
  - id: embedding-default
    type: embedding
    default: true
    embedding_dim: 1024        # 必须与模型实际输出一致
    batch_size: 32
    max_tokens: 512
    upstream: { base_url: https://api.example.com/v1, model: some-embedding-model, api_key_env: EMB_API_KEY }
  - id: reranker-default       # 可选
    type: reranker
    default: true
    upstream: { base_url: https://api.example.com/v1, model: some-reranker, api_key_env: RERANK_API_KEY }
```

| 接口 | 调用方 |
|---|---|
| `POST /api/private/mf-model-api/v1/chat/completions` | BKN Agent、执行工厂、Context Loader |
| `POST /api/mf-model-api/v1/chat/completions` | studio |
| `POST /api/private/mf-model-api/v1/small-model/embeddings` | bkn-backend、Vega、执行工厂 |
| `POST /api/private/mf-model-api/v1/small-model/reranker` | Context Loader |
| `GET /api/private/mf-model-manager/v1/small-model/get` | bkn-backend、Vega |
| `GET /api/private/mf-model-manager/v1/small-model/get_by_name` | bkn-backend |
| `GET /api/private/mf-model-manager/v1/small-model/get_default` | bkn-backend、执行工厂 |
| `GET /api/private/mf-model-manager/v1/small-model/list` | 执行工厂、studio |
| `GET /api/private/mf-model-manager/v1/prompt/{id}` | 执行工厂（仅在配置了提示词 ID 时） |

要点：

- 对话接口要支持流式（以 `data: [DONE]` 结尾）、工具调用、`response_format`、`reasoning_content` 的透传。请求未带 `max_tokens` 时不补默认值，交给上游。
- 模型名为空时使用该类型的默认模型；执行工厂按名称 `embedding` 查找 embedding 模型，替身要能按名称和类型解析。
- 启动时对 embedding 模型实际调用一次，校验输出维度与 `embedding_dim` 一致，不一致则拒绝启动。
- 未配置 reranker 时，按原顺序返回、分数递减。
- bkn-backend 与 ontology-query 的配置中打开默认小模型（`defaultSmallModelEnabled: true`），使概念索引带上向量。

studio 的模型管理页面在 mini 中隐藏。

### 4.4 Trace

Context Loader 的 MCP 业务调用必须带会话编号，编号只能由 Trace 核心服务发放；Trace 回复"采集关闭"、出错或不可达时，MCP 调用会被拒绝，执行工厂运行的函数也拿不到会话编号，读 BKN 的函数因此失败。所以替身必须始终表现为"运行正常、采集开启"，收到的数据直接丢弃。

| 接口（前缀 `/api/agent-observability/v1`） | 行为 |
|---|---|
| `POST /conversations:ensure-current` | 返回会话 |
| `POST /conversations/{cid}/interactions` | 返回交互，ID 与请求一致，`capture_enabled: true`，带租约字段 |
| `GET /interactions/{iid}` | 返回交互与租约 |
| `POST /conversations/{cid}/interactions/{iid}/operations:ensure` | 返回操作（`attempt ≥ 1`）、`receipt_status: "pending"`、`execute: true` |
| `POST /operations/{oid}/attempts` 及 `:complete`、`:fail` | 返回成功 |
| `GET /operations/{oid}`、`GET /receipts/{rid}` | 返回对应记录 |
| `POST /interactions/{iid}/finish` | ID 与状态与请求一致 |
| `GET /operations/__rest_context_probe__` | 404 |
| 证据上传地址（`BKN_TRACE_ARTIFACT_ENDPOINT`） | 任意 2xx |

替身绝不能返回 `capture_disabled`、`capture_enabled: false` 或 5xx。替身先按不存状态实现（编号由请求内容推算），由第 9 节的流程测试验证；不够时再加内存状态。

各服务的 Trace 发布开关保持关闭；BKN Agent 需显式设置 `BKN_TRACE_EVIDENCE_PUBLISHER_ENABLED=false`、`OTEL_ENABLED=false`、`OTEL_LOGS_ENABLED=false`。

### 4.5 消息

平台只在"重命名"时同步发一条消息（知识网络、数据目录、连接器类型、工具箱、MCP 改名），发送失败会使请求失败。平台公共消息库同时支持 Kafka 与 NSQ，NSQ 发送就是 `POST /pub?topic=...`。mini 把相关服务的 `mqType` 设为 `nsq` 并指向 mini-core，替身收下即丢弃。不运行 Kafka 或 NSQ。

这部分配置只能通过挂载配置文件修改（bkn-backend、Vega 的 `depServices.mq`，执行工厂的 `mq_config.yaml`）。Vega 的 `kafka-connect` 配置项必须保留，否则启动失败。

### 4.6 函数执行器

执行工厂通过沙箱控制面运行函数。mini 用一个 Python 进程实现执行工厂用到的接口，以子进程运行函数。平台在 Kubernetes 上同样以子进程方式运行（不隔离），行为一致。

| 接口（前缀 `/api/v1`） | 用途 |
|---|---|
| `POST /sessions` | 创建会话（执行工厂启动时预建 3 个） |
| `GET /sessions/{id}` | 查询会话与已装依赖 |
| `GET /sessions` | 管理页面 |
| `DELETE /sessions/{id}` | 删除会话 |
| `POST /executions/sessions/{id}/execute-sync` | 运行函数或 shell，同步返回 |
| `POST /sessions/{id}/dependencies/install` | 安装依赖 |
| `POST /sessions/{id}/files/upload` | 上传并解压 Skill 包 |

要点：

- 复用沙箱自带的 `sandbox_sdk` 与代码包装逻辑；镜像中预装 `bkn-osdk`（来自 GitHub 固定版本）。
- 结果从标准输出最后一行 JSON 解析；状态用小写（`completed`、`failed`、`timeout`）；超时返回 200 加 `timeout` 状态，不返回 408；不返回 `execution_time` 字段。
- 每次运行用新进程；超时结束整个进程组。
- 依赖按"包索引 + 依赖集合"缓存，安装串行执行。
- 传给函数的环境变量只包含执行工厂传入的变量与 `BKN_BASE_URL`、`BKN_SANDBOX_MCP_URL`，不继承 mini 自身的环境。
- 工作目录为容器内 `/workspace/<会话>`。

## 5. 启动与配置

启动顺序：

1. compose 等待 `mariadb`、`opensearch` 健康检查通过；
2. `migrator` 执行建表或增量迁移后退出；
3. `mini` 启动：mini-core 先起替身与网关，再依次拉起 Redis、函数执行器、各平台服务，等待各自就绪；
4. 脚本提示访问地址。

各服务配置放在 openbkn-mini 仓库中，按平台 Helm 模板生成本地版本。要点：

| 配置 | 值 |
|---|---|
| `BKN_SAFE_URL` / `BKN_SAFE_BASE_URL` / `BKN_SAFE_BASE` | 指向 mini-core（注意各服务对是否带路径的约定不同） |
| Hydra 地址（`depServices.hydra-admin`、`oauth.admin_host` 等） | 指向 mini-core |
| 模型服务地址 | 指向 mini-core |
| Trace 核心地址与证据上传地址 | 指向 mini-core |
| 消息 `mqType` | `nsq`，指向 mini-core |
| Trace、审计发布开关 | 关闭 |
| Redis | 容器内，只放内存，不落盘 |
| OpenSearch | 单节点；关闭安全插件 |
| studio | 嵌入模式 |

studio 中隐藏系统管理、模型管理、Trace、指标相关入口。隐藏方式优先用 studio 现有的模块与权限配置；如需改 studio，单独提出。

## 6. 数据

| 数据 | 位置 |
|---|---|
| 元数据（网络、对象类、工具箱、Agent 等） | `mariadb` 卷 |
| 索引、向量、Vega 导入的数据集 | `opensearch` 卷 |
| 函数依赖缓存、Skill 文件 | `mini` 卷 |
| 锁、缓存 | Redis 内存，重启丢失不影响 |

Vega 导入的数据集只存在于 OpenSearch 中，无法从别处重建，因此备份必须同时包含两个卷。备份方式：停止服务后打包三个卷。

## 7. 使用方式

```bash
git clone https://github.com/sh00tg0a1/openbkn-mini.git
cd openbkn-mini
cp models.example.yaml models.yaml   # 填写模型地址与密钥
./mini.sh up        # 启动并等待就绪
./mini.sh status    # 各服务状态
./mini.sh logs [服务]
./mini.sh backup    # 停止后打包数据卷到本地目录
./mini.sh down
```

- 浏览器打开 `http://localhost:8000` 使用 studio。
- 命令行：`BKN_BASE_URL=http://localhost:8000 BKN_TOKEN=mini openbkn bkn list`，令牌可为任意值。
- Agent：安装 `openbkn` 与 `create-bkn` 技能，按上述方式指向 mini。
- MCP：`http://localhost:8000/api/agent-retrieval/v1/mcp`。

最低资源（估计，实测后修正）：内存 4GB 可用于 Docker，其中 OpenSearch 约 1GB。

## 8. 迁移到社区版

按模型迁移，使用现有命令：

1. 知识网络：`openbkn bkn pull` 从 mini 导出，`openbkn bkn push` 推送到社区版；
2. 工具箱、MCP、Skill：用执行工厂的导入导出；
3. 数据源：在社区版中重新连接，重建索引；Vega 导入的文件需重新导入；
4. 模型：在社区版模型工厂中登记，按需调整引用。

整库迁移暂不支持。mini 使用平台内置超级管理员 ID、模型使用固定 ID，以后如需整库迁移，可在此基础上开发迁移工具。

## 9. 测试

1. **替身接口测试**：用 bkn-safe 的契约样例（`bkn-safe/contract/testdata/`）与 `docs/api/` 中的接口定义校验替身返回的格式。
2. **完整流程测试**（CI）：启动 compose，依次推送样例网络、导入数据、查询与 Cypher、语义检索、执行一个读 BKN 的函数、调用 MCP 工具、与 BKN Agent 对话一轮、重命名知识网络。CI 中的模型用返回固定内容的假模型，保证结果可重复。
3. **版本更新**：每次更新平台版本先跑通第 2 步再发布。

实施的第一步是可行性验证：只拉起 MariaDB、OpenSearch、bkn-backend、ontology-query、Vega、Context Loader 与相关替身，跑通"发布知识网络 → 导入数据 → 查询 → MCP 调用"，重点验证代理账号与 Trace 两个替身。通过后再接入执行工厂、函数执行器、BKN Agent 与 studio。

## 10. 风险与限制

| 项 | 说明 | 应对 |
|---|---|---|
| 代理账号与授权同步 | 在发布知识网络的路径上，返回值校验严格，同步语义未完全核实 | 可行性验证优先覆盖 |
| Trace 替身 | 编号与状态需满足 Context Loader 的检查 | 可行性验证优先覆盖；必要时加内存状态 |
| 依赖平台未使用的 NSQ 分支 | 若平台删除该分支，重命名会失败 | 流程测试覆盖重命名；届时改为接受报错或加单节点 Kafka |
| 配置跟随 | 平台 Helm 模板变化需手工同步 | 版本更新清单 + 流程测试 |
| 可执行文件可移植性 | 从官方镜像拷出的程序需能在 mini 镜像中运行（静态链接或依赖一致） | 构建时校验；不行则 mini 以对应官方镜像为基础分层 |
| 函数无隔离 | 函数可访问容器内全部内容 | 文档写明仅限本机、可信代码 |
| `bkn-osdk` 来自 GitHub | 构建镜像需访问 GitHub | 构建时下载并固定版本 |
| 资源占用 | OpenSearch 约 1GB 内存，首次启动几十秒 | 文档写明；脚本显示启动进度 |

## 11. 以后可能做

- `openbkn mini` 子命令（bkn-sdk），替代 bash 脚本；
- 用 SQLite（FTS5 与向量）替换 OpenSearch，减少内存与容器数；
- 整库迁移到社区版的工具；
- pip 或 npm 安装的启动薄壳。
