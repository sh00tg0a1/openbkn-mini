# openbkn mini 设计

## 1. 目标

openbkn mini 是给开发者在自己机器上使用的 OpenBKN。它保留建模、数据接入、查询与检索这些核心能力，去掉认证与权限，所有平台 Go 服务合在一个进程里运行，用 Docker 启动，不需要 Kubernetes。

开发者用它来：

- 用 BKN 建模，由 Agent（Claude Code、Codex 等）编写 `.bkn` 文件并推送；
- 接入数据库，查询实例、沿关系遍历、执行 Cypher；
- 做全文与语义检索，让 Agent 通过 MCP 使用知识网络。

### 1.1 范围

| 第一版包含 | 第一版不包含 |
|---|---|
| 建模：网络、对象类、关系类、行动类（只定义）、概念组、值类型 | 认证、用户、权限管理 |
| 数据接入（Vega）：连接数据库、发现表、批量构建索引 | Vega 流式构建 |
| 查询：实例查询、关系遍历、Cypher | 指标与指标分析 |
| 检索：全文、语义（配置 embedding 模型时） | 执行工厂：工具、函数、MCP 服务、Skill、行动执行 |
| Context Loader（MCP），供外部 Agent 使用 | BKN Agent 对话 |
| studio 界面（建模与查询相关页面） | BKN Trace、模型工厂 |

不包含的执行工厂与 BKN Agent 留到第二期；第 11 节说明原因。

### 1.2 原则

1. **复用平台代码，不另写一套。** mini 直接引用平台服务的代码，行为与平台一致。
2. **不修改平台代码。** 多个服务共处一个进程的问题，全部在 mini 内处理（第 3.3 节）。
3. **替身只做最小实现。** 认证、权限、模型、Trace、消息由替身代替，替身不做真实鉴权与采集。
4. **装和跑要简单。** 一条命令启动，一份模型配置文件。

## 2. 总体架构

mini 由 3 个容器组成，用 docker compose 编排：

| 容器 | 镜像 | 内容 |
|---|---|---|
| `mini` | openbkn-mini 构建 | 一个 Go 进程（平台服务、替身、网关、studio 页面）与一个 Redis 进程 |
| `mariadb` | MariaDB 官方镜像 | 平台元数据库 `openbkn` |
| `opensearch` | 平台发布的 OpenSearch 镜像（带 IK 中文分词） | 索引、向量、Vega 数据集 |

另有一个一次性的 `migrator` 容器，使用平台的 data-migrator 镜像，在 `mini` 启动前建表或执行增量迁移，完成后退出。

### 2.1 mini 进程的组成

| 组成 | 来源 | 说明 |
|---|---|---|
| bkn-backend | 平台代码 | 建模、概念索引、Cypher |
| ontology-query | 平台代码 | 实例查询、关系遍历 |
| vega-backend | 平台代码 | 数据目录、发现、索引构建 |
| Context Loader | 平台代码 | MCP 与检索 |
| 替身 | 新写 | Hydra、bkn-safe、模型、Trace、消息，见第 4 节 |
| 网关 | 新写 | 对外唯一端口，按平台网关的路径转发；托管 studio 页面 |

Redis 以子进程运行，只放内存，由 mini 进程拉起与看护。

各服务在进程内仍各自监听原来的端口（只绑定 `127.0.0.1`），彼此按 HTTP 调用，与平台部署时相同；对外只暴露网关端口。

## 3. 单进程装配

### 3.1 引用方式

平台各服务的启动代码位于可引用的 `app` 包中，提供 `Boot`（准备）与 `Run`（开始服务）。mini 是一个独立的 Go 模块，引用这四个服务与平台公共库，固定到同一个平台版本。各服务的第三方依赖版本一致，可以直接编译进同一个程序。

### 3.2 启动顺序

1. 设置进程级环境变量（bkn-safe、Trace 等地址指向替身）；
2. 启动替身与 Redis，等待端口就绪；
3. 依次调用四个服务的 `Boot`；
4. 依次调用 `Run`，每个服务在独立的协程中运行，等它的端口就绪后再启动下一个。顺序为 Vega、ontology-query、Context Loader、bkn-backend：bkn-backend 启动时会调用 Vega，Vega 未就绪会导致启动失败；
5. 启动网关；
6. 收到停止信号后，各服务自行关闭，mini 等待片刻后退出。

### 3.3 不修改平台代码的处理

mini 不修改平台代码。多个服务共处一个进程时的三处障碍，在 mini 内处理：

| 障碍 | mini 的处理 |
|---|---|
| 每个服务启动时都会"冻结"版本授权登记，第二次冻结会使进程崩溃 | mini 通过 Go 的 `go:linkname` 访问公共库中的冻结标记，在每个服务启动前清除。服务按顺序启动，清除只发生在两个服务之间 |
| 服务监听端口失败时会退出整个进程 | mini 启动前检查所有端口是否可用，有冲突则报出端口号并退出，不启动任何服务 |
| 各服务自行处理停止信号 | mini 收到停止信号后等待各服务关闭，再退出进程 |

另外，平台版本号在编译 mini 时注入，否则命令行读到的是源码默认值，版本检查不通过。这是构建参数，不改代码。

`go:linkname` 依赖公共库内部变量的名称。若平台改名或删除该变量，mini 在链接阶段即编译失败，不会在运行时出错；更新平台版本时据此调整一行代码。

### 3.4 共用一个进程带来的影响

以下是全进程只有一份的设置，接受现状，在文档中说明：

- **日志**：只有一个全局日志器，级别以最后加载的服务配置为准，日志中的服务名可能与实际来源不符；
- **数据库连接池**：按第一个服务的配置建立，各服务配置必须指向同一个库；
- **链路追踪与审计标签**：以最后设置的服务为准。mini 默认关闭这些功能。

## 4. 替身

所有替身都把调用者当作平台内置的超级管理员：

- 用户 ID：`266c6a42-6131-4d62-8f39-853e7093701c`
- 角色：`super_admin`（`7dcfcc9c-ad02-11e8-aa06-000c29358ad6`）

使用这个 ID，mini 中创建的资源归属与平台初始管理员一致，为以后整库迁移到社区版留下余地。

### 4.1 Hydra

| 接口 | 行为 |
|---|---|
| `POST /admin/oauth2/introspect` | 任何令牌（包括空令牌）都返回有效的超级管理员 |

返回体中 `active`、`sub`、`scope`、`client_id`（与 `sub` 不同），以及 `ext` 中的 `visitor_type`、`login_ip`、`udid`、`account_type`、`client_type` 缺一不可。平台公共库读取这些字段时不做类型检查，缺字段会使进程崩溃。

### 4.2 bkn-safe

| 接口 | 返回 |
|---|---|
| `POST /api/safe/v1/authz/checks` | 每项允许；结果与请求顺序一致、逐项回显；顶层允许 |
| `POST /api/safe/v1/authz/resource-filter` | 回显全部资源；需要操作列表时按权限注册表给出该类型的全部操作 |
| `GET /api/safe/v1/authz/resources` | 不限制范围 |
| `POST /api/safe/v1/authz/row-filters` | 每个对象类一条，条件为"不限制" |
| `POST /api/safe/v1/authz/property-levels` | 每个属性完全可见 |
| `POST`、`DELETE /api/safe/v1/authz/policies` | 成功 |
| `PUT`、`DELETE`、`GET /api/safe/v1/authz/resource-parents` | 写操作成功；查询返回空 |
| `POST /api/safe/v1/api-keys/introspect` | 无效（mini 不签发 AppKey） |
| `POST /api/safe/v1/directory/names` | `user_names`、`app_names` 为 `{id, name}` 数组；超级管理员返回固定名称，其余返回 ID |
| `POST /api/safe/v1/directory/users-detail`、`GET /api/safe/v1/directory/users/{id}` | 超级管理员的信息 |
| `GET /api/safe/v1/internal/license/current` | 404，各服务按社区版运行 |
| `/api/safe/in/v1/managed-proxy-accounts` 系列 | 不存状态：账号 ID 由知识网络 ID 推出，创建、查询、停用、恢复、归档都现场生成符合校验的账号 |
| `/api/safe/in/v1/proxy-grant-sources` 系列 | 一律允许；同步类接口返回"无变化" |
| `GET /api/safe/v1/me`、`/me/permissions`、`/me/authorization-registry`、`/capabilities` | 供 studio 使用：超级管理员、全部权限、平台权限注册表、社区版 |

权限注册表使用平台 bkn-safe 自带的种子文件，随平台版本更新。

### 4.3 模型

mini 读取 `models.yaml`，实现平台模型服务的接口，把请求转发到 OpenAI 兼容地址：

```yaml
models:
  - id: embedding-default      # 固定 ID，平台数据中按此引用
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
  - id: llm-default            # 可选，供 Context Loader 推断逻辑属性参数
    type: llm
    default: true
    upstream: { base_url: https://api.example.com/v1, model: some-chat-model, api_key_env: LLM_API_KEY }
```

| 接口 | 调用方 |
|---|---|
| `POST /api/private/mf-model-api/v1/small-model/embeddings` | bkn-backend、Vega |
| `POST /api/private/mf-model-api/v1/small-model/reranker` | Context Loader |
| `POST /api/private/mf-model-api/v1/chat/completions` | Context Loader |
| `GET /api/private/mf-model-manager/v1/small-model/{get,get_by_name,get_default,list}` | bkn-backend、Vega |

要点：

- 未配置任何模型时，查询类接口返回"没有模型"，检索只用全文；
- 未配置 reranker 时，按原顺序返回，分数递减；
- 配置了 embedding 模型时，启动时实际调用一次，输出维度与 `embedding_dim` 不一致则拒绝启动；并打开 bkn-backend 的默认小模型开关，使概念索引带上向量。

### 4.4 Trace

Context Loader 的 MCP 业务调用必须带会话编号，编号由 Trace 核心服务发放。Trace 回复"采集关闭"、出错或不可达时，MCP 调用会被拒绝。因此替身始终表现为"运行正常、采集开启"，收到的证据直接丢弃，会话与操作状态只存在内存中。

| 接口（前缀 `/api/agent-observability/v1`） | 行为 |
|---|---|
| `POST /conversations:ensure-current` | 返回会话 |
| `POST /conversations/{cid}/interactions` | 返回交互，采集开启，带租约 |
| `GET /interactions/{iid}` | 返回交互与租约 |
| `POST /conversations/{cid}/interactions/{iid}/operations:ensure` | 返回操作与待签发回执，允许执行 |
| `POST /operations/{oid}/attempts` 及 `/attempts/{n}:complete`、`:fail` | 返回成功 |
| `GET /operations/{oid}`、`GET /receipts/{rid}` | 返回对应记录 |
| `POST /interactions/{iid}/finish` | 状态取请求中的 `outcome`（`cancelled` 记为 `canceled`），与请求一致 |
| 证据上传地址 | 任意 2xx |

结束会话的确认必须与请求一致，否则 Context Loader 会重试直到超时（约 60 秒）。

### 4.5 消息

平台只在重命名（知识网络、数据目录、连接器类型）时同步发送一条消息，发送失败会使请求失败。平台公共消息库支持 NSQ，发送即 `POST /pub?topic=...`。mini 把 bkn-backend 与 Vega 的消息类型配置为 NSQ 并指向替身，替身收下即丢弃。

## 5. 网关与路径

网关对外开放一个端口（默认本机 `8000`），按前缀转发：

| 前缀 | 服务 |
|---|---|
| `/api/bkn-backend/` | bkn-backend |
| `/api/ontology-query/` | ontology-query |
| `/api/vega-backend/` | Vega |
| `/api/agent-retrieval/` | Context Loader（含 MCP `/api/agent-retrieval/v1/mcp/`） |
| `/api/safe/` | bkn-safe 替身 |
| `/api/mf-model-api/`、`/api/mf-model-manager/` | 模型替身 |
| 其余 | studio 页面 |

MCP 地址以 `/` 结尾；不带结尾斜杠会被重定向。

### 5.1 studio

studio 不修改代码，按以下方式接入：

- **构建**：使用固定版本的 studio 源码构建，构建时设置 `VITE_USE_MOCK=false`，否则页面显示 studio 内置的演示数据。产物以 `/studio/` 为基础路径，网关在 `/studio/` 下托管，根路径重定向到 `/studio/`。
- **免登录**：网关生成 `/studio/config.js`，设置 `mode: "hosted"`。这是 studio 为"不部署 bkn-safe"准备的运行方式，关闭登录，所有请求不带令牌，由 Hydra 替身视为超级管理员。
- **菜单裁剪**：同一份 `config.js` 给本地用户只配建模（不含指标）、数据目录与数据资源相关的权限，并将其标记为非超级管理员。studio 按权限过滤菜单，模型管理、系统管理等入口随之隐藏。
- **授权提示**：bkn-safe 替身的能力接口返回 `fallback_community`（按社区版运行），studio 不显示"产品尚未激活"的提示。

以下入口没有权限或能力门控，仅靠权限无法隐藏：

| 位置 | 入口 |
|---|---|
| 顶部菜单 | 项目管理、执行工厂、可观测性 |
| 知识网络工作区 | 智能问答、函数、API、MCP、SKILLs |

为此给 studio 增加一个运行时开关：`config.js` 中列出要隐藏的模块，studio 不显示这些模块的菜单与工作区入口，也不发起它们的接口请求。开关缺省为空，平台部署不受影响。这是 mini 唯一需要的 studio 改动，单独提交到 studio 仓库。

## 6. 配置与数据

### 6.1 各服务配置

mini 为每个服务提供一份本地配置文件，地址全部指向 `127.0.0.1` 上的服务、替身与容器端口。要点：

| 配置 | 值 |
|---|---|
| bkn-backend 端口 | 13015（避开 Vega 的 13014） |
| 数据库 | `mariadb` 容器，库 `openbkn` |
| OpenSearch | `opensearch` 容器，关闭安全插件 |
| Redis | 进程内 Redis，单机模式 |
| 消息 | NSQ，指向替身 |
| Trace、审计发布 | 关闭 |
| bkn-safe、Hydra、模型、Trace 核心地址 | 指向替身 |

这些配置随平台版本更新时，对照平台 Helm 模板同步。

### 6.2 数据

| 数据 | 位置 |
|---|---|
| 元数据（网络、对象类、数据目录等） | `mariadb` 卷 |
| 索引、向量、Vega 数据集 | `opensearch` 卷 |
| 锁、缓存、会话 | Redis 内存，重启丢失不影响 |

Vega 数据集只存在于 OpenSearch 中，备份必须同时包含两个卷。

### 6.3 建表与升级

首次安装时，每个服务执行最新版本目录下的 `init.sql`；升级时执行高于已装版本的增量脚本。mini 使用平台的 data-migrator 执行这一过程，保持与平台一致的迁移记录，便于以后整库迁移。涉及的服务：bkn-backend、vega-backend，以及两个 Trace 发件箱表（服务代码引用但不写入）。

## 7. 使用方式

```bash
git clone https://github.com/sh00tg0a1/openbkn-mini.git
cd openbkn-mini
cp models.example.yaml models.yaml   # 可选：配置 embedding、reranker
./mini.sh up        # 启动并等待就绪
./mini.sh status
./mini.sh logs
./mini.sh backup    # 停止后打包数据卷
./mini.sh down
```

- 浏览器打开 `http://localhost:8000` 使用 studio。
- 命令行：`BKN_BASE_URL=http://localhost:8000 BKN_TOKEN=mini openbkn bkn list`，令牌可为任意值。
- Agent：安装 `openbkn` 与 `create-bkn` 技能，按上述方式指向 mini；MCP 地址为 `http://localhost:8000/api/agent-retrieval/v1/mcp/`。

### 7.1 检索前的索引配置

Vega 为一张表构建索引前，要求该表配置主键字段与增量字段；平台中这一步在 studio 完成。mini 提供 `./mini.sh index <知识网络 ID>`：对网络中已绑定数据的对象类，以对象类的主键作为表的主键与增量字段、IK 分词作为全文分析器，配置并构建索引。

### 7.2 资源占用

实测（供应链样例，12 张表、约 7.8 万行）：

| 组成 | 内存 |
|---|---|
| mini 进程 | 约 105MB |
| OpenSearch | 约 950MB |
| MariaDB | 约 180MB |
| Redis | 约 35MB |

建议给 Docker 分配至少 4GB 内存。

## 8. 构建与版本

- openbkn-mini 以子模块引入 bkn-foundry 与 bkn-studio，固定到同一平台版本的提交。
- `mini` 镜像在构建阶段编译 mini 程序（引用子模块中的平台代码），注入平台版本号；构建 studio 并以嵌入模式打包进网关。
- mini 的版本号与平台一致。平台发布后，mini 跑通第 10 节的测试再发布。

## 9. 迁移到社区版

按模型迁移，使用现有命令：

1. 知识网络：`openbkn bkn pull` 从 mini 导出，`openbkn bkn push` 推送到社区版；
2. 数据源：在社区版中重新连接数据库，重建索引；
3. 模型：在社区版模型工厂中登记，按需调整引用。

整库迁移暂不支持。

## 10. 测试

1. **替身契约测试**：以 bkn-safe 的契约样例与平台接口定义校验替身返回格式。
2. **完整流程测试**（CI）：启动 compose，依次执行：建数据目录并发现表、导入样例网络、绑定数据、实例查询、一跳与两跳 Cypher、配置索引并全文检索、通过 MCP 开始会话—`run_cypher`—结束会话、重命名知识网络。CI 中的 embedding 与 reranker 使用返回固定结果的假模型。
3. **版本更新**：每次更新平台版本，先跑通第 2 步再发布。

## 11. 第二期：执行工厂与 BKN Agent

第一版不包含执行工厂与 BKN Agent，原因：

- 执行工厂与 Context Loader 重复登记了 5 条通用错误文案，两者编入同一程序会在程序初始化时崩溃。这发生在 mini 的任何代码运行之前，无法在 mini 内绕过；
- 执行工厂的启动代码位于 `main` 包，无法被引用，mini 只能复制约 150 行启动代码；
- 执行工厂与 Context Loader 使用的 MCP 库版本不同，编入同一程序会被统一到较高版本；
- 两者读取同名的观测配置文件；
- 执行函数需要沙箱替身（以子进程运行 Python），BKN Agent 本身是 Python 服务，二者会把 Python 运行环境带进 mini。

在不修改平台代码的前提下，第二期的可选做法是让执行工厂作为 mini 容器内的第二个进程运行（不与其他服务合并），BKN Agent 同样以独立进程运行。届时再评估。

## 12. 风险与限制

| 项 | 说明 | 应对 |
|---|---|---|
| 依赖平台内部实现 | 冻结标记的清除依赖内部变量名；替身返回格式依赖平台的接口细节 | 变量变化在编译时暴露；替身由契约测试与流程测试覆盖 |
| 一个服务出错影响整个进程 | 一个服务崩溃或监听失败会使整个进程退出 | 启动前检查端口；mini 进程由 Docker 自动重启 |
| 依赖平台未使用的 NSQ 分支 | 若平台删除该分支，重命名会失败 | 流程测试覆盖重命名 |
| 配置跟随 | 平台 Helm 模板变化需手工同步 | 版本更新清单与流程测试 |
| 资源占用 | OpenSearch 约 1GB 内存，首次启动需数十秒 | 文档写明；脚本显示启动进度 |
| 仅限本机 | 无认证与权限 | 网关默认只监听本机地址；文档写明不得暴露到网络 |

## 13. 以后可能做

- 用 SQLite 替换 MariaDB，用 SQLite（FTS5 与向量）替换 OpenSearch，最终只剩一个容器，乃至无需 Docker 的单个程序；
- `openbkn mini` 子命令，替代 bash 脚本；
- 整库迁移到社区版的工具。
