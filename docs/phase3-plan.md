# Phase 3：持久调度与人工控制

## 已交付

1. `:start` 在同一事务内把 Task 置为 `queued`、记录 Outbox 与审计，并创建持久 Planner 唤醒。后台调度器可由多个服务进程同时运行。
2. Planner 唤醒通过数据库租约和 fence 串行化，Outbox 消费按 `(consumer_name,event_id)` 去重，500ms 合并唤醒。Planner 每轮读取图、Goal、Scope、Constraint、Todo、Intent、事件、预算和空闲 Worker 数。租约过期或执行失败可重排。
3. Worker 领取使用 `FOR UPDATE SKIP LOCKED`；任务并发、全局上限、优先级、创建顺序和任务轮转在数据库事务中执行。领取、创建 Run、增加 attempt 和写入 30 秒租约同事务提交；10 秒续租，过期回收，最多 3 次自动尝试。旧 Run 和 Activity 保留，迟到写入受 fence 拒绝。
4. 观察源必须实现 `ObserveOnce(..., idempotencyKey)`；稳定 key 与持久检查点确保租约重试复用同一观察，人工重试提高 generation 并允许新的观察。Fact、Intent 终态、Run 终态和 Outbox 同事务提交。生产环境尚未接入真实工具，Intent 会给出明确阻塞原因，不能凭模型文本生成 Fact。
5. 暂停、恢复、取消、Intent 纠偏、停止与重试均通过受管 API 和审计执行。暂停不领取新 Intent；Worker 在观察后及提交前检查任务状态。停止会废止租约，保留原因；重试使用新 Run ID。
6. Planner 可持久化共享 Todo 与依赖；依赖未完成的下游 Intent 不派发。Planner 可用 `steers` 和 `stops` 纠偏/终止方向，停止必须附原因。Worker 会收到人工 steer 和同任务历史 Trace。Trace 搜索默认支持排除当前 Run，完整 Transcript 可继续按 Run ID 查询。
7. Token 与工具调用计数原子累加，预算或截止时间达到时 Intent 进入 `exhausted`，Worker 保存阶段性结构化结论。超出模型窗口时保留完整原消息，另存可追溯摘要视图；范围、Goal、Todo、Intent 和人工指令不被裁剪。
8. 任务页提供自动刷新、状态文案、暂停/恢复/取消、Todo 依赖、纠偏/停止/重试和执行记录搜索；桌面与窄屏浏览器均已检查。

## 验收证据

- `TIANFA_TEST_DATABASE_URL=... go test ./...`：包含 20 个并发领取者、全局上限、任务轮转、优先级、最大尝试、暂停恢复、并发工具预算预留、预算耗尽、Todo 依赖、停止原因、Goal 再入队、Planner cursor、上下文压缩。
- `TestKilledWorkerProcessRecovers`：实际杀掉持有租约的子进程；将数据库时钟条件推进到到期后，Reconciliation 回收，下一 Run 以 attempt 2 接管，旧 Run 留为 interrupted。
- `TestObservationCheckpointAndExplicitRetryGeneration`：进程中断后的重复领取只调用一次带幂等键的观察源；人工重试增加 generation 并产生一次新的观察。
- `make test`、`make build`、OpenAPI 漂移检查通过。
- 浏览器：创建任务、配置本地测试模型、排队到运行、生成待验证方向、暂停恢复、Todo 前置依赖、纠偏与停止表单；桌面和 390px 窄屏无横向溢出。

## 阶段边界

Phase 4 才接入真实工具、Scope/Policy 强制链和完整 ToolCall 执行。当前生产 Worker 在无受控观察源时只会标记 `blocked`；Phase 3 的幂等观察源接口与数据库 fence 是后续工具的前置契约。模型费用定价与 `max_cost_minor` 的精确控制仍需价格配置；当前可执行的预算限制是输入/输出 Token、工具调用数和截止时间。
