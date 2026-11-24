# 需求追踪

状态含义：`已实现` 只表示当前 Phase 的代码与测试已落地；`计划` 表示依赖后续 Phase。

| 需求 | Phase | 状态 | 实现位置 | 验证 |
| --- | --- | --- | --- | --- |
| REQ-CLEAN-001..006 | 0 | 已实现 | `docs/adr/0001-product-identity.md`、全新代码边界 | 仓库审查 |
| REQ-DEPLOY-001 | 0 | 已实现 | `cmd/server`、`internal/webui`、`Dockerfile` | `make build` |
| REQ-DEPLOY-002 | 0 | 已实现 | `internal/config` | 配置单元测试 |
| REQ-DEPLOY-003 | 0 | 已实现 | `compose.yaml` | Compose 配置校验 |
| REQ-DEPLOY-004 | 0 | 已实现 | `internal/store/postgres/migrate.go` | 独立测试数据库并发及重复迁移测试、Compose 验证 |
| REQ-DEPLOY-005 | 0 | 已实现 | `internal/api/health.go` | Handler 单元测试 |
| REQ-DEPLOY-006 | 0 | 部分实现 | `cmd/server/main.go` | 信号退出；运行中任务收场在 Phase 3 |
| REQ-UI-001 | 0 | 已实现 | `web/app` | 文本、图标和颜色共同表达状态 |
| REQ-UI-002..007 | 6 | 计划 | — | 对应 E2E |
| REQ-DATA-001..003 | 1 | 已实现 | `internal/task/activity.go`、`domain_events`/`event_consumptions`/`activities` | `TestPhase1Flow` 事务与重复投影 |
| REQ-ASSET-001..005 | 1 | 已实现 | `internal/task/assets.go`、`assets`、`task_asset_links` | `TestPhase1Flow` 规范化、去重、关联和资产图查询 |
| REQ-GRAPH-001..005 | 1..6 | 部分实现 | `internal/task/store.go`、图迁移及锚点 API | Phase 1 根节点、Goal 血缘、锚点、覆盖已验收；Worker 事实和压缩在后续 Phase |
| REQ-STATE-001..004 | 1..3 | 部分实现 | `internal/task/control.go` | 草稿乐观锁与启动前置条件已验收；真实运行状态在 Phase 3 |
| REQ-PROD-001 | 1..3 | 部分实现 | `internal/task`、`web/app/tasks` | 草稿创建及目标/范围/约束/预算已实现；模型链与执行控制待 Phase 2/3 |
| REQ-PROD-002 | 1 | 已实现 | `assets`、`exploration_nodes/edges/anchors` | `TestPhase1Flow`、浏览器详情页 |
| REQ-PROD-003..010 | 2..7 | 计划 | 见阶段计划 | 对应后续 Phase 验收 |

每完成一个 Phase，本表必须扩展到该阶段触及的全部需求号，并附上测试证据。

Phase 0/1 的 OpenAPI 契约由 `internal/api/openapi_contract.go` 与 `internal/api/openapi_phase1.go` 生成，
`go run ./cmd/openapi -check` 在 CI 检查漂移。开发前端将健康请求转发至 Go 服务。

