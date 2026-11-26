# 天罚分阶段实现计划

本文是规格第 18 章要求的执行计划。一次只声明一个 Phase 完成；后续阶段不得用
内存 Mock、前端假数据或 Prompt 约定替代真实持久化和程序校验。

## Phase 0 — 骨架与契约（已验收）

- Go 模块化单体、Next.js 静态导出、Docker Compose 与 PostgreSQL。
- PostgreSQL advisory lock 保护的内嵌迁移框架。
- `/health/live`、`/health/ready`、JSON 结构化日志、请求 ID、RFC 9457 风格错误。
- OpenAPI 基线、CI、生产多阶段镜像。
- Go `embed` 提供前端静态资源；前端以真实健康状态引导首次使用。

验收：空系统可构建并启动；迁移可重复执行；静态前端由同一 Go 进程提供。

## Phase 1 — 身份、任务与双图（已验收）

- 单组织初始化、Session/CSRF/RBAC。
- Task、Goal、Constraint、Scope 聚合与受管状态转换。
- 资产图、探索图、锚点、Transactional Outbox。
- UI 完成任务创建、范围确认、任务列表和图/覆盖查询。

验收证据与 Phase 2 模型链边界见 [phase1-plan.md](phase1-plan.md) 和 [ADR-0002](adr/0002-draft-without-llm-profile.md)。

## Phase 2 — LLM 与 Agent Runtime（已验收）

- Secret Store、Profile Chain、限速、计量、熔断与主备切换。
- Goal Decomposer、Planner、Worker、提示词版本和 Transcript。
- Fake Provider 驱动可重复的 Plan → Intent → Fact 集成测试。

详细范围、阶段边界与测试证据见 [phase2-plan.md](phase2-plan.md)。

## Phase 3 — Durable Engine（核心能力已验收）

- Intent Queue、Lease、调度公平性、暂停恢复、Reconciliation。
- Todo、事件唤醒、Trace、Steer/Kill、上下文压缩。

设计、验收证据与 Phase 4 边界见 [phase3-plan.md](phase3-plan.md)。

## Phase 4 — Tool、Scope、Skill 与 MCP（核心能力已验收）

- Tool 装饰链和真实命令/文件工具。
- Scope/Policy 强制层、审批、Skill 延迟加载、MCP、自定义 HTTP Tool。

验收证据与安全边界见 [phase4-plan.md](phase4-plan.md)。用户取消任务预算配置的决定见 [ADR-0003](adr/0003-remove-task-budgets.md)。

## Phase 5 — Traffic、Evidence 与 Finding

- 记录代理、内容寻址 Blob、Finding、不可变证据、复测和报告导出。

## Phase 6 — 完整 UI

- 规格第 13 章全部页面、SSE 去重合并、图可视化、ToolCall/证据血缘。

## Phase 7 — 加固与发布

- 并发/故障注入、性能、恢复演练、可访问性、SBOM、签名和运维文档。

