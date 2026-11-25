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
| REQ-DEPLOY-006 | 0..3 | 已实现 | `cmd/server/main.go`、`internal/agent/scheduler.go` | 信号取消运行；过期租约由 Reconciliation 接管 |
| REQ-UI-001 | 0 | 已实现 | `web/app` | 文本、图标和颜色共同表达状态 |
| REQ-UI-002..007 | 6 | 计划 | — | 对应 E2E |
| REQ-DATA-001..003 | 1 | 已实现 | `internal/task/activity.go`、`domain_events`/`event_consumptions`/`activities` | `TestPhase1Flow` 事务与重复投影 |
| REQ-ASSET-001..005 | 1 | 已实现 | `internal/task/assets.go`、`assets`、`task_asset_links` | `TestPhase1Flow` 规范化、去重、关联和资产图查询 |
| REQ-GRAPH-001..005 | 1..6 | 部分实现 | `internal/task/store.go`、`internal/agent`、图迁移及锚点 API | 根节点、Goal、Intent、Fact 血缘、锚点、覆盖已验收；Finding/压缩在后续 Phase |
| REQ-STATE-001..009 | 1..3 | 已实现 | `internal/task`、`internal/agent/control.go`、`scheduler.go`、Phase 3 迁移 | 受管状态机、租约、暂停恢复、attempt 上限、Goal 再入队；故障注入测试 |
| REQ-PROD-001 | 1..3 | 部分实现 | `internal/task`、`internal/llm/chain.go`、`web/app/tasks` | 目标/范围/约束/Token 与工具预算、模型链、调度控制已实现；费用预算精确计量待价格配置 |
| REQ-PROD-002 | 1 | 已实现 | `assets`、`exploration_nodes/edges/anchors` | `TestPhase1Flow`、浏览器详情页 |
| REQ-PROD-007 | 2 | 已实现 | `internal/llm/profile.go`、`chain.go`、`web/app/models` | `TestPhase2ProfileAPI`、`TestPlanIntentFactAndQuotaFailover` |
| REQ-LLM-001..008 | 2 | 已实现 | `internal/llm`、`internal/secret`、`llm_profiles`/`llm_attempts` | `TestSharedLimiterAndHalfOpenProbe`、`TestPartialStreamContinuesOnBackup`、`TestPhase2ProfileAPI` |
| REQ-AGENT-001..005 | 2 | 已实现 | `internal/llm/prompt.go`、`internal/agent/engine.go`、`agent_messages` | Prompt 版本/哈希与不可信观察集成断言 |
| REQ-PLAN-001..008 | 2..3 | 已实现 | `internal/agent/engine.go`、`scheduler.go`、`proof.go` | Planner 租约、Outbox 去重唤醒、方向去重、Todo 依赖、并发限制、结构化阻塞、证明门槛、纠偏/停止 |
| REQ-WORK-001、002、004 | 2 | 已实现 | `internal/agent/engine.go` | 受控观察源 Fact 血缘、无观察不确认事实、恢复测试 |
| REQ-WORK-003 | 4 | 计划 | — | 工具 Schema/RBAC/Scope/Policy 装饰链 |
| REQ-WORK-005..007 | 3..4 | 部分实现 | `internal/agent/traces.go`、`scheduler.go`、`compaction.go` | 同任务 Trace、人工 steer、预算耗尽结构化结论与完整原文已实现；模型主动调用 Trace 工具及完整多回合 settlement 随 Phase 4 工具循环接入 |
| REQ-ENG-001..006 | 3 | 已实现 | `internal/agent/scheduler.go`、`000004_phase3.sql` | 20 并发领取、任务公平性、事务内 Run/Lease、子进程 Kill 后接管、孤儿 Run 回收 |
| REQ-PROD-003、006 | 3..6 | 部分实现 | `internal/agent`、`web/app/tasks` | Intent/Run/Trace/控制已实现；完整 Agent 工作台与实时 SSE 在 Phase 6 |
| REQ-PROD-004、005、008..010 | 4..7 | 计划 | 见阶段计划 | Tool、Finding、证据、报表与发布验收 |

每完成一个 Phase，本表必须扩展到该阶段触及的全部需求号，并附上测试证据。

Phase 0/1/2/3 的 OpenAPI 契约由 `internal/api/openapi_contract.go`、`internal/api/openapi_phase1.go`、`internal/api/openapi_phase2.go` 与 `internal/api/openapi_phase3.go` 生成，
`go run ./cmd/openapi -check` 在 CI 检查漂移。开发前端将健康请求转发至 Go 服务。

