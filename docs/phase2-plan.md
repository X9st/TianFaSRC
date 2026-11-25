# Phase 2 需求与验收计划

目标：管理员配置加密凭据、模型 Profile 和主备链；任务能够记录可恢复的 Agent Run 与 Transcript。在受控 HTTP 假 Provider 和假观察源下完成 Decomposer → Planner → Intent → Worker → Fact，并证明 402/429 后按顺序切换 Profile、独立计量且不重复写图。

| 需求 | 实现边界 | 验收 |
| --- | --- | --- |
| REQ-LLM-001..003 | 数据库共享限速状态、熔断状态与 half-open 探针 | 并发路由、冷却与恢复集成测试 |
| REQ-LLM-004..007 | 分类故障、Run 关联的 failover 事件、部分流继续、无可用 Profile 时 blocked | 本地 HTTP Stub 的 402/429/流中断测试 |
| REQ-LLM-008 | AES-GCM Secret Store、只回显引用和尾号、换主机需重交密钥 | API 回显、数据库密文、权限及日志测试 |
| REQ-AGENT-001..005 | 版本化角色正文、代码不变量、分离动态 Context、标记不可信观察 | Prompt 哈希与注入测试 |
| REQ-PLAN-001、003、006、007 | 单任务 Planner 锁、结构化 Intent、近似去重、规划阻塞与证明门槛 | 假 Provider 脚本化端到端测试 |
| REQ-WORK-001、002、004 | 单 Intent Worker、受控观察源、结构化 Fact 写回 | Fact 血缘、无观察不得确认事实测试 |
| REQ-PROD-007 | Task 显式链与系统默认链；同模型不同 Key 为独立 Profile | 主 Key 配额耗尽自动切备用 |

交付顺序：

1. 迁移、Secret Store、Profile CRUD/默认链/Task 链、前端配置入口。
2. 三种 HTTP Provider 适配、共享限速、健康状态、计量、故障转移及流继续。
3. Prompt 版本、Transcript、Decomposer/Planner/Worker 结构化动作和事务图写回。
4. 真实 PostgreSQL + 本地受控 HTTP Stub 集成测试、浏览器验收、OpenAPI 与部署文档。

阶段边界：Phase 4 前没有真实目标工具和程序化 Scope Enforcer。生产 Worker 不得将模型文本当成目标事实；未提供受控观察时 Intent 进入可恢复的 blocked，界面明确显示原因。Phase 2 验收中的 Fact 由测试注入的受控观察源产生。Phase 3 接管持久队列、租约、公平调度、Todo 依赖与事件唤醒；本阶段只实现必要的单次可审计运行入口。REQ-WORK-003 的工具调用校验由 Phase 4 完成。

## 验收记录

- `TestPlanIntentFactAndQuotaFailover`：真实 PostgreSQL、主备 HTTP 假 Provider、402 配额故障转移、Prompt 版本、Transcript、不可信观察、Plan→Intent→Fact 血缘、Goal 证明门槛；生产观察源缺失时无 Fact，受控观察恢复后继续。
- `TestSharedLimiterAndHalfOpenProbe`：20 个并发请求共用每秒 2 次窗口；软错误阈值开路、冷却、唯一 half-open 探针与恢复。
- `TestPartialStreamContinuesOnBackup`：部分流的已交付内容持久化，备用 Profile 接收继续回合消息；全部不可用时返回可恢复 blocked 和最早冷却时间。
- `TestAgentSubchainStaysWithinTaskChain`：角色子链只能选任务链内的 Profile；任务链缩减后不保留越界角色路由。
- `TestProviderFormatsAndPartialStream`：OpenAI Chat、OpenAI Responses、Anthropic Messages 与流中断解析。
- `TestPhase2ProfileAPI`：CSRF、管理员写权限、密钥不回显、跨主机换 Key、在用 Profile 删除冲突。
- `TIANFA_TEST_DATABASE_URL=... make test`、`make build`、桌面与手机端浏览器视觉验收。
