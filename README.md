# 天罚 TianFa

面向明确授权安全团队的自主漏洞挖掘平台。产品从资产与授权范围出发，将多 Agent 探索、真实工具观察、漏洞线索、不可变证据和复测报告连接成可追溯的工作流。项目严格以
[`全新自主安全验证平台-核心能力实现规格.md`](./全新自主安全验证平台-核心能力实现规格.md)
为产品事实来源，按 Phase 逐步实现。

当前交付：**Phase 6 核心工作台；Phase 7 加固进行中**。总览按漏洞线索、已确认漏洞和挖掘任务组织入口；任务、探索图、会话、工作区文件、策略与审计、流量与证据均有界面。任务页通过事件流及时刷新，并展示每个 Worker Run 的独立额度及消耗。额度由 Planner 按探索方向给出和调整，服务端限制模型回合、工具次数、Token 与运行时长，用户无需配置。产品定位见 [ADR-0005](docs/adr/0005-vulnerability-discovery-positioning.md)，验收边界见 [Phase 6 验收记录](docs/phase6-plan.md) 和 [Phase 7 计划](docs/phase7-plan.md)。

## 本地启动

完整环境需要 Docker Desktop：

```bash
docker compose up --build
```

打开 <http://localhost:8080>。健康检查：

```bash
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
```

首次部署在交互终端初始化唯一的管理员：

```bash
docker compose exec app tianfa-server init-admin
```

输入邮箱和至少 12 字节的密码后，打开 `/login/` 登录。创建任务需要至少一个可验证目标及一条 Allow 授权范围。任务详情显示探索图血缘、资产锚点与覆盖；全局资产页可添加和关联资产，管理员可在用户页创建、停用和调整角色。停用用户会立即撤销现有会话。生产环境请在 HTTPS 反向代理后访问控制台；纯 HTTP 登录只允许本机回环地址。

如果 `8080` 已被 Burp 等本地工具占用，可以只更改宿主机端口：

```bash
TIANFA_HOST_PORT=18080 docker compose up --build
```

此时打开 <http://localhost:18080>；容器内部和应用默认端口保持不变。

本地开发也可以分别启动前后端：

```bash
make dev-server
make dev-web
```

后端默认监听 `:8080`，前端开发服务器监听 `:3000`，通过开发模式转发健康检查。
后端使用其他地址时，设置 `TIANFA_DEV_API_ORIGIN`，例如
`TIANFA_DEV_API_ORIGIN=http://127.0.0.1:18080 make dev-web`。
初始化管理员的源码开发命令是 `go run ./cmd/server init-admin`。未配置 PostgreSQL 时后端仍可存活，但 `/health/ready` 会返回未就绪。

管理员登录后在 `/models/` 添加模型 Profile 和 API Key，再设置系统默认主备链。相同模型的不同 Key 需分别创建 Profile；任务详情可覆盖主备链，并为目标拆解、规划器、执行者选择链内子链。启动任务会进入持久队列，页面自动刷新进度，并记录 Decomposer、Planner、Intent、Prompt 版本、Transcript、模型用量与真实 ToolCall。可在任务详情暂停、恢复、取消、纠偏、停止或重试方向，并管理有依赖的共享步骤。管理员在 `/tools/` 配置工具、Skill、MCP 与审批；Worker 仅凭真实工具观察写入确认 Fact。

在 `/findings/` 选择任务后可查询流量、创建 Finding 草稿、绑定和排序不可变证据、复核确认、记录复测及导出报告。记录代理默认监听本机 `127.0.0.1:8081`，Compose 宿主机也只映射回环地址。下载本地 CA，在受控测试客户端配置代理认证：用户名 `tianfa`，密码使用页面生成的一小时任务令牌。代理当前支持范围内无正文的 GET/HEAD。复测目前由人工采集新流量并提交验证结论，尚未接入自动 Retester Agent；详见 [Phase 5 验收记录](docs/phase5-plan.md)。

隔离命令工具需显式配置 `TIANFA_TOOL_IMAGE` 为预装工具的容器镜像，默认不开放。MCP stdio 同样使用本机 Docker 无网络容器。远端 MCP 需由管理员确保其服务端自身的网络出口受控；本平台只能校验传给它的显式目标 URL，详见 [Phase 4 验收记录](docs/phase4-plan.md)。

API Key 使用数据目录 `secrets/master.key` 加密保存，该文件权限为 `0600`；部署备份必须同时保留数据库和此密钥文件。若数据库已存在加密凭据而密钥文件丢失，服务会拒绝启动。生产环境应限制数据目录访问。

## 备份与恢复

Compose 部署可运行 `make backup` 创建包含 PostgreSQL、Blob、工作区、Skill 和密钥材料的冷备份。脚本会短暂停止应用写入，在备份完成后恢复应用。备份目录包含敏感密钥，须限制访问并安全异地保存。恢复前先对备份运行 `make restore-drill BACKUP_DIR=/path/to/backup`；演练会在隔离的临时数据库中校验 Blob 哈希、引用计数和加密凭据，并启动服务检查就绪。完整部署、恢复和运维步骤见 [运维手册](docs/operations.md)。

## 验证

```bash
make test
make build
make vet
make race
```

OpenAPI 由 Go 契约定义生成：`make openapi` 更新文档，
`make openapi-check` 检查文档与源码一致。PostgreSQL 迁移集成测试需要
`TIANFA_TEST_DATABASE_URL`；CI 中默认运行。

需求追踪和阶段计划见 [docs/requirements-traceability.md](docs/requirements-traceability.md)
与 [docs/implementation-plan.md](docs/implementation-plan.md)。Phase 5 验收结果见 [docs/phase5-plan.md](docs/phase5-plan.md)。

