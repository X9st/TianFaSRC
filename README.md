# 天罚 TianFa

面向明确授权安全团队的自主安全验证平台。项目严格以
[`全新自主安全验证平台-核心能力实现规格.md`](./全新自主安全验证平台-核心能力实现规格.md)
为产品事实来源，按 Phase 逐步实现。

当前交付：**Phase 3 — Durable Engine**。持久队列、租约恢复、Planner 事件唤醒、共享 Todo、Trace 与人工控制已可用；真实目标工具和范围强制执行在 Phase 4 接入。

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

管理员登录后在 `/models/` 添加模型 Profile 和 API Key，再设置系统默认主备链。相同模型的不同 Key 需分别创建 Profile；任务详情可覆盖主备链，并为目标拆解、规划器、执行者选择链内子链。启动任务会进入持久队列，页面自动刷新进度，并记录 Decomposer、Planner、Intent、Prompt 版本、Transcript 和模型用量。可在任务详情暂停、恢复、取消、纠偏、停止或重试方向，并管理有依赖的共享步骤。真实目标工具尚未接入时，Worker 将 Intent 标为“待工具验证”，不会把模型推断写成确认 Fact。

API Key 使用数据目录 `secrets/master.key` 加密保存，该文件权限为 `0600`；部署备份必须同时保留数据库和此密钥文件。若数据库已存在加密凭据而密钥文件丢失，服务会拒绝启动。生产环境应限制数据目录访问。

## 验证

```bash
make test
make build
```

OpenAPI 由 Go 契约定义生成：`make openapi` 更新文档，
`make openapi-check` 检查文档与源码一致。PostgreSQL 迁移集成测试需要
`TIANFA_TEST_DATABASE_URL`；CI 中默认运行。

需求追踪和阶段计划见 [docs/requirements-traceability.md](docs/requirements-traceability.md)
与 [docs/implementation-plan.md](docs/implementation-plan.md)。Phase 3 验收结果见 [docs/phase3-plan.md](docs/phase3-plan.md)。

