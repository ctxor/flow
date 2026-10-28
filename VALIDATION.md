# 本机验证记录

验证日期：2026-10-25，Asia/Shanghai。

## 已执行并通过

- Go：17个测试，使用真实 PostgreSQL 隔离 schema；`go test -race ./... -count=1`。
- Python：10个测试，含真实本地 gRPC 认证/规划调用及模型 HTTP 适配器模拟测试。
- `go vet ./...`，前端 JavaScript 语法检查，Compose 配置校验。
- Linux/arm64、CGO关闭的 API 交叉编译。
- 完整原生服务联调：HTTP → Redis Streams → Go Worker → Python gRPC → PostgreSQL任务/提醒 → 到时站内通知。
- 浏览器实际登录、自然语言创建任务、标记完成、自然语言保存私有备忘、SSE刷新。

使用环境：Go 1.27.3、Python 3.9.6、Redis 7.4.6。数据库验证使用本机已缓存的 PostgreSQL 19rc1 镜像；仓库标准 Compose 配置指定 PostgreSQL 17。17版本的容器全栈运行尚未在本机验证。

## 当前运行方式

应用：<http://127.0.0.1:8080>。浏览器预览中是自动化验证创建的临时账号；你可以退出后注册自己的账号。

本机验证使用原生 Go API、Go Worker、Python Agent与 Redis；PostgreSQL容器名为 `flow-assistant-local-postgres`。现在可以用 `make dev` 启动原生完整服务。临时验证账号和测试结果位于被忽略的 `work/` 中，不会提交到 Git。没有配置真实模型、飞书或微信凭证，不会向真实平台发送通知。

由于 Docker 镜像下载的网络连接失败，本次没有完成整个 Compose 镜像构建。为继续验证，使用现有数据库镜像并从 Redis 官方发行源码构建本地 Redis；其状态保存在 `work/redis-data`。切换为标准 Compose 运行前，需要先停止当前本地进程和这个数据库容器，避免端口冲突。`make down` 只停止标准 Compose 管理的容器。

本机验证服务端口：HTTP 8080、Agent 50051、PostgreSQL 55432、Redis 56379，均仅绑定回环地址。

## 仍需真实账号验证

- 选定模型的可用性、自然语言效果、时延和费用；当前默认是规则演示模式。
- 飞书应用实际权限、发布审核和真实收发。加密回调、签名、持久化去重已用构造事件测试。
- 微信账号权限、用户互动条件、出口IP设置和获准模板。安全模式回调和去重已测试；主动提醒当前提供单文本字段模板适配，需要按实际模板结构调整。

详细启动与配置见 `README.md`。

## 2026-10-27 调试启动修复验证

- 修复从 `cmd/api`、`cmd/worker` 启动时无法加载根目录 `.env` 的问题；已有环境变量仍优先。
- 新增6组配置测试，覆盖命令子目录启动、环境变量优先级、显式配置文件、缺失文件、模块边界及纯环境变量部署。
- 已实际从 `cmd/api` 启动 API 并验证 `/readyz`，不依赖终端预先导出 `AGENT_TOKEN`。
- `make dev` 已实际启动本机 PostgreSQL、Redis、Python Agent、Go API 和 Worker，并通过真实 gRPC 创建任务、8秒提醒的完整检查。原有数据库被复用，未清空。
- 验证 Ctrl+C 会退出本次启动的应用和本机依赖进程，原有数据库容器继续运行；`make dev-deps` 也已验证。
- Go 集成测试（含竞态检测）、`go vet`、Python 启动脚本语法和 VS Code JSON 配置检查通过。VS Code 完整调试配置会先准备依赖，再启动 API 和 Worker。
