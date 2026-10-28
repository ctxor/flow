# 拾序 Flow · AI 个人效率助手

一个可运行的 Go + Python 项目：网页、飞书应用机器人与微信服务号共用业务能力，通过自然语言创建任务、保存备忘、安排单次日程与提醒。业务入口位于 `cmd/`。

## 已实现

- 注册、登录、HttpOnly 会话 Cookie，以及供 HTTP 客户端使用的 Bearer Token。
- 个人与共享空间；所有者、编辑者、只读成员；任务指派与评论。
- 任务创建、完成、重开、标题修改与改期，使用版本号检测修改冲突。
- 私有/空间共享备忘，关键词搜索、删除；单次日程、参与人、取消。
- PostgreSQL 持久化提醒，后台扫描、取消、随任务改期；站内通知、空间变更与 SSE 实时刷新。
- Outbox + Redis Streams 异步处理；消费幂等、执行租约、失败重试、数据库补偿恢复。
- 独立 Python gRPC 规划服务，Go 负责权限、业务校验与实际写入。
- 飞书加密回调、微信明文/加密回调、持久化去重、一次性绑定码和渠道发送适配。
- 简单网页工作台、Docker Compose、SQL 迁移、Go 集成测试与 Python/gRPC 测试。

默认 `AI_MODE=demo` 是保守的规则演示，不调用语言模型。配置 `AI_MODE=openai` 后调用支持 Chat Completions 的模型接口，使用结构化输出并做本地参数校验。模型与平台密钥由你提供，不会写入仓库。

## 本机快速启动（推荐）

在项目根目录执行：

```sh
make dev
```

启动器会复用现有 PostgreSQL 数据库，启动 Redis、Python Agent、Go Worker 和 API。看到 `FLOW_READY` 后打开 <http://127.0.0.1:8080>。终端需要保持运行；按 Ctrl+C 停止本次启动的进程，已经存在的数据库不会被删除或停止。启动日志保存在 `work/dev-logs/`。

首次在新机器运行时，先启动 Docker Desktop，再执行 `make deps` 安装 Go/Python 依赖。PostgreSQL、Redis 不存在时会尝试用 Compose 启动；本机有 Redis 可执行文件时优先使用本机实例。

## VS Code / Delve 调试

1. 用 VS Code 打开**项目根目录**，不要只打开 `cmd/api`。
2. 如果终端已在运行 `make dev`，先按 Ctrl+C 停止，避免 API 端口冲突或额外 Worker 消费掉断点对应的作业。
3. 在“运行和调试”的下拉列表选择 **Flow: 完整调试（API + Worker）**，按 F5。
4. 自动启动的“Flow: 启动依赖”任务会准备数据库、Redis 和 Python Agent；API、Worker 则由调试器启动。
5. 网页地址仍是 <http://127.0.0.1:8080>。手动创建任务的断点放在 `CreateTask`，AI 后台流程的断点放在 Worker 的 `AgentJob` / `apply`。

也可以先在终端运行 `make dev-deps`，再选择 `Flow: API` 或 `Flow: Worker` 调试单个进程；只有 API 时，网页的基本增删改查可用，AI 作业和到期提醒还需要 Worker。停止 Go 调试后，依赖任务可能继续运行，可从 VS Code“终端 → 终止任务”停止“Flow: 启动依赖”。

完整调试的依赖任务只执行一次，就绪后才并行启动两个 Go 进程，参考 [VS Code 复合调试配置](https://code.visualstudio.com/docs/debugtest/debugging-configuration)。

两个 Go 入口会从当前目录向上查找 `.env`，到 `go.mod` 所在目录为止，因此从 `cmd/api` / `cmd/worker` 启动也能加载根目录配置。可用 `ENV_FILE` 指定明确的配置文件，已有进程环境变量优先。`.vscode/launch.json` 同时显式设置了根目录 `cwd` 和 `envFile`。

如果出现 `AGENT_TOKEN must have at least 16 characters`，请检查根目录 `.env` 是否存在、同名环境变量是否被设为空，以及调试配置是否使用了错误工作目录。`make init` 只创建缺失的配置，不会覆盖已有密钥。

## Docker 一键运行

需要 Docker Compose、Make 和 Python 3。首次执行会生成 `.env` 和随机内部服务凭证，不覆盖已有配置。

```sh
make up
```

打开 <http://127.0.0.1:8080>，自行注册账号。没有默认管理员账号或固定登录密码。

```sh
docker compose logs -f api worker agent
make down
```

数据库和 Redis 数据保存在 Docker volumes 中，`make down` 不删除数据。首次拉取镜像或构建依赖需要网络。默认端口仅绑定本机；公网部署需配置 HTTPS 与 `COOKIE_SECURE=true`。

## 本地开发

需要 Go 1.27.3+、Python 3.9+。Protobuf 生成代码已提交；只在修改协议时需要 `protoc`。

```sh
make init
make deps
docker compose up -d --wait postgres redis
```

在三个终端分别启动：

```sh
make agent
make worker
make api
```

Go 入口与 Python 服务均自动读取项目根目录 `.env`，已有环境变量优先。默认本机地址：HTTP `127.0.0.1:8080`，gRPC `127.0.0.1:50051`，PostgreSQL `127.0.0.1:55432`，Redis `127.0.0.1:56379`。不要同时启动占用相同端口的本地与完整容器版本。

## 使用示例

演示模式支持以下自然语言：

```text
明天下午三点交周报，提前十分钟提醒我
一分钟后提醒我起来活动
创建任务：整理下周分享材料
记一下：分享会可以聊最近读的书
明天下午三点开会，持续一小时，提前十分钟提醒我
查看今天的任务
汇总任务
```

日期或时长不明确时会追问；同一用户、空间和入口中，15分钟内的下一条消息可补充上一次未完成请求。演示模式对周几、重复日程等复杂表述会要求改用明确日期，不会猜测。

## 接入模型

修改 `.env`：

```dotenv
AI_MODE=openai
AI_BASE_URL=https://api.openai.com/v1
AI_API_KEY=你的密钥
AI_MODEL=你有权访问的模型标识
```

默认接口需要支持 `response_format.json_schema`。仅支持 JSON 模式的兼容网关可设置 `AI_RESPONSE_FORMAT=json_object`；本地白名单、类型检查与 Go 业务校验仍会执行。配置修改后重启 agent；容器环境用 `docker compose up -d agent`。

协议位于 `proto/agent.proto`，只有一个 `Plan` RPC，返回 Protobuf `oneof` 操作：创建任务、保存备忘、创建日程、查询任务、汇总或追问。AI 无数据库凭证，不能任意执行 SQL、代码或网络工具。身份和空间来自 Go 的已认证上下文。

内部 RPC 使用 `AGENT_TOKEN` 认证及显式 deadline；容器内只开放于私有网络。本机开发只监听回环地址。跨机器部署时应增加 TLS/mTLS。模型请求会传输用户的当前输入给配置的服务商；本实现不自动上传所有备忘或任务库。

结构化输出实现参考：[OpenAI 官方文档](https://developers.openai.com/api/docs/guides/structured-outputs?api-mode=chat)。

## 飞书应用机器人

1. 在飞书开放平台创建企业自建应用，启用机器人能力。
2. 配置 `.env` 中的 `FEISHU_APP_ID`、`FEISHU_APP_SECRET`、`FEISHU_VERIFICATION_TOKEN`、`FEISHU_ENCRYPT_KEY`。
3. 使用 **Webhook 加密回调**，地址为 `https://你的域名/callbacks/feishu`；本实现没有启用长连接模式。
4. 订阅 `im.message.receive_v1`，申请接收用户单聊消息和以应用身份发送消息所需权限，按实际后台要求发布应用。
5. 登录网页，在“平台接入”生成绑定码，在机器人私聊中发送 `绑定 绑定码`。

当前机器人仅处理文本私聊，默认操作该账号最早创建的个人空间。群聊事件忽略，避免把个人数据发送到群里；共享空间在网页操作。发送使用平台消息 UUID，减少重试带来的重复通知。回调只有在事件与 Outbox 一起持久化后才应答。

参考：[机器人能力](https://open.feishu.cn/document/client-docs/bot-v3/bot-overview)、[事件投递与重发](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/event-subscription-guide)。

## 微信服务号

1. 准备拥有所需接口权限的公众号/服务号，配置服务器回调 `https://你的域名/callbacks/wechat`。
2. 设置 `WECHAT_APP_ID`、`WECHAT_APP_SECRET`、`WECHAT_TOKEN`；安全模式设置 `WECHAT_AES_KEY`，与公众号后台保持一致。
3. 设置服务端出口 IP 白名单等账号要求。
4. 在网页生成绑定码，向公众号发送 `绑定 绑定码`。

签名校验、消息解密使用 `silenceper/wechat` SDK。耗时规划在后台完成，交互结果通过客服消息发送；实际能否发送依赖账号权限与用户互动条件。

主动提醒必须配置符合业务场景且获准使用的 `WECHAT_TEMPLATE_ID`。当前提供一个文本字段映射 `WECHAT_TEMPLATE_FIELD`，默认 `thing1`，值取通知摘要前20字。**这是单字段模板适配器**：如果你的获准模板要求多个字段，需要扩展 `internal/platform/platform.go` 的模板构造后再启用。没有模板时站内提醒正常保留，微信渠道记录失败原因，不把客服消息当作无限制主动通知通道。

微信不确定是否成功的发送记录为 `unknown`，不自动重试，避免重复打扰。未提供真实账号凭证时，只能验证签名、解密、去重与发送适配逻辑，不能验证账号实际发送能力。

## HTTP API

除健康检查、注册/登录及平台回调外，接口均需会话 Cookie 或 `Authorization: Bearer <access_token>`。注册响应包含 token；网页只使用 HttpOnly Cookie，不将 token 存入 localStorage。

| 方法与地址 | 功能 |
| --- | --- |
| `POST /v1/auth/register`、`POST /v1/auth/login` | 注册/登录 |
| `POST /v1/auth/logout`、`GET /v1/me` | 登出、当前用户 |
| `GET/POST /v1/workspaces` | 列出/创建空间 |
| `GET/POST /v1/workspaces/:id/members` | 查询成员、添加或调整已注册成员权限 |
| `GET/POST /v1/tasks` | 查询/创建任务 |
| `PATCH /v1/tasks/:id` | 修改标题、截止时间、状态，必须带当前 `version` |
| `GET/POST /v1/tasks/:id/comments` | 任务评论 |
| `GET/POST /v1/notes`、`DELETE /v1/notes/:id` | 备忘，查询支持 `q` |
| `GET/POST /v1/events`、`DELETE /v1/events/:id` | 单次日程及取消 |
| `GET /v1/reminders`、`DELETE /v1/reminders/:id` | 查询/取消自己的提醒 |
| `POST /v1/assistant/messages` | 提交规划，返回202和作业ID，支持 `Idempotency-Key` |
| `GET /v1/agent-jobs/:id` | 查询自己的作业状态 |
| `GET /v1/notifications?after=0` | 按ID升序补拉通知，每批100条 |
| `GET /v1/stream?after=0` | SSE，支持 `Last-Event-ID` |
| `GET /v1/bindings`、`POST /v1/bindings/codes` | 绑定记录与一次性绑定码 |
| `DELETE /v1/bindings/:provider` | 解除绑定 |
| `GET /v1/deliveries` | 查询自己的渠道投递状态 |
| `GET /healthz`、`GET /readyz` | 进程存活、数据库/Redis就绪检查 |

任务/备忘/日程列表传 `workspace_id`。列表第一版最多返回200条，不包含完整分页。日期采用带偏移的 RFC3339；数据库保存时间点，用户 IANA 时区用于自然语言解析和显示。网页的手动时间输入采用浏览器本机时区。

创建任务示例：

```json
{
  "workspace_id": "你的空间UUID",
  "title": "交周报",
  "due_at": "2026-10-26T15:00:00+08:00",
  "remind_before_minutes": 10
}
```

`due_at`、`remind_before_minutes` 和 `assignee_id` 均可选；要求提醒时必须提供未来时间。将任务标记完成会取消相关提醒；重开任务不会自动恢复已取消提醒。

## 可靠性设计

业务数据、提醒、接收事件、通知、投递任务和待发布事件保存在 PostgreSQL。事件发布到 Redis 后标记 published；发布和标记之间崩溃可以重复发布，消费者通过业务状态和租约去重。

每秒扫描到期提醒，使用短事务和 `FOR UPDATE SKIP LOCKED` 并发领取。任务改期增加提醒版本；投递前检查版本和取消状态。正在外部网络调用中的通知无法撤回，因此完成/取消与实际发送之间仍有小范围并发窗口。

消费组确认、过期消息接管、每15秒数据库补偿扫描共同处理重启和队列丢失。发送临时失败退避重试，最多5次；永久错误停止；`accepted` 只表示平台已受理，不能等同于用户已读。SSE直接按用户读取持久化通知，可断线续传，不依赖易丢失的 Pub/Sub。

## 测试

```sh
make test
make integration
```

`make test` 运行纯测试和 Python gRPC 测试；没有 `TEST_DATABASE_URL` 时 Go 的数据库测试会明确跳过。集成测试创建随机 PostgreSQL schema 并在结束后只删除该测试schema，不清空应用库。队列测试使用内存 Redis 实例；完整本地联调使用真实 Redis Streams。

也可针对已有本地 PostgreSQL：

```sh
TEST_DATABASE_URL='postgres://assistant:assistant@127.0.0.1:55432/assistant?sslmode=disable' go test -race ./internal/... -count=1
```

测试覆盖空间越权、私有备忘隔离、任务冲突、事务回滚、绑定码复用和账号劫持、重复回调、提醒改期、投递重试与不确定状态、AI作业重放、SSE用户隔离、gRPC认证、中文相对时间与夏令时歧义。

修改协议后执行 `make generate`。SQL 迁移由入口自动执行并使用事务锁串行化；后续结构变更应增加迁移版本，不直接依赖 AutoMigrate。

运行中的完整服务还可以执行 `python3 scripts/smoke.py`：它创建一个临时测试账号，通过真实 Go→Python gRPC 创建8秒后的提醒，并检查站内提醒确实到达。测试结果保存在被忽略的 `work/`，不会调用真实模型或平台发送接口（前提是使用默认 demo 模式且未为这个临时账号绑定渠道）。

## 项目边界

当前是可运行的第一版，并未实现重复日程、外部日历同步、向量检索、附件、批量AI操作或任意工具执行。日程目前支持创建、查询和取消，修改可取消后重建；没有硬性禁止日程重叠。任务汇总读取当前空间最近200条任务并生成确定性的统计，不是对全量历史的模型分析。

多人协作采用空间权限和任务版本，不包含实时共同编辑、邀请邮件或完整组织管理。通知、会话、Outbox和队列尚未设置定期保留清理；长期部署应增加归档、备份与运行指标。数据库/Redis就绪检查不代表外部平台或模型账号可用。
