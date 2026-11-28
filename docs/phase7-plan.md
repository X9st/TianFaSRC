# Phase 7：加固与发布计划

状态：进行中。第 17 章的全部门槛未通过之前不得宣布发布完成。

## 7A：产品定位与基础质量门禁

- 统一漏洞挖掘口径和前端任务、线索、证据、复测入口；待复核与已确认结果分开计数。
- 总览只查询优先级最高的 5 条待复核线索，并为组织内状态统计和线索队列建立索引。
- 工作台加入键盘跳转主要内容入口，表单保留可见焦点；复查桌面与窄屏。
- CI 加入 `go vet ./...` 和 `go test -race ./...`，使用独立测试 PostgreSQL。

已验证：`make test`、`make build`、`make vet`、带 PostgreSQL 的 `make race`；桌面与 390px 窄屏无横向溢出，Tab→Enter 可跳到主要内容，浏览器控制台无错误。截图：[桌面](qa/phase7-mining-dashboard-desktop.png)、[窄屏](qa/phase7-mining-dashboard-mobile.png)。

## 7B：完整端到端与故障注入

- 在受控授权靶场执行规格 E2E-01 至 E2E-06，覆盖完整自主挖掘、主备模型、范围阻断、证据存续、崩溃恢复与 Skill/MCP。
- 补齐第 17.3 节尚未覆盖的事务失败、数据库中断和控制命令竞态。

已有自动化证据：`TestConcurrentClaimsRecoveryAndFencing`（20 Worker 单 Lease）、`TestPlanIntentFactAndQuotaFailover`（真实 HTTP Stub 的主备切换）、`TestPartialStreamContinuesOnBackup`、`TestToolCallIdempotencyScopeAndAudit`、`TestMCPDeferredUntilSkillLoad`（延迟解锁、连接故障后再次调用、大输出预览截断与完整读取）、`TestEvidenceSurvivesTrafficDeletionAndRejectsTampering`、`TestKilledWorkerProcessRecovers`，以及新增的 `TestFailedTrafficTransactionLeavesCollectableOrphans`、`TestPauseCancelAndReconcileRaceHasOneTerminalReason`、`TestTransientDatabaseOutageDoesNotCompleteTask`。并发测试发现 Reconciliation 曾读取过时的任务状态，导致取消后 Intent 重开；现对任务与 Lease 同时加行锁，重复 20 轮竞态测试通过。数据库断连测试验证运行不会误报完成，恢复后过期 Lease 可重新领取。这些是场景的组成部分，尚不足以替代六个从 UI 到数据和报告的完整 E2E。

新增 `TestPhase7AutonomousHTTPFindingAndFailoverE2E`：从管理员 API 登录、创建两目标/授权范围、启动任务，到 Planner 并行派工、两个 Worker 访问本地靶场并创建可追溯 Fact/Finding、证明目标、任务完成，检查 API 状态、图、Finding 和 Activity。主 HTTP Stub 返回 402，备用 Profile 接管，独立尝试/用量及 UI 所用 Activity 接口均通过断言；连续 3 次重跑通过。现已在同一测试中补上真实 HTTP 流量的 baseline/proof 绑定、人工确认、报告保存、普通流量删除后的 Evidence 原字节读取与证据 ZIP 导出，以及解除绑定后的 Blob 回收；Linux 容器和独立 PostgreSQL 下通过。它覆盖 E2E-01/02/04 的后端主链路，仍需纯浏览器完整操作、Worker 新资产写入和串行 Todo 等场景验收。

新增 `TestInjectedPageAndRedirectCannotExpandWorkerScope`：本地靶场页面诱导 Worker 访问越界环回地址，模型 Stub 照此发起请求；同一 Worker 随后遇到越界重定向，再改用范围内路径完成观察。两条越界路径均被程序阻断并写入审计，审计包含脱敏的原始请求目标、被阻断目标、解析 IP、命中的规则及原因。该测试覆盖 E2E-03 的后端主链路；浏览器展示与更多网络通路仍需单独验收。

## 7C：发布加固

- 性能基线、可观测指标、安全与可访问性审计。
- PostgreSQL、Blob、Workspace、Skills、Secret 材料的一致性备份及恢复演练。
- SBOM、镜像签名、部署与运维文档，以及最终 Definition of Done 复核。

已落地：Compose 冷备份脚本、受限归档解包、隔离恢复演练、Blob 文件哈希/大小/引用计数及凭据解密校验；恢复后服务 ready。2026-11-27 在隔离项目中用 1 条流量 Blob 引用、1 条加密凭据和工作区文件实际演练通过，结果 `blobs_verified=1`、`traffic_refs=1`、`secrets=1`。归档安全测试进入 CI，运维步骤见 [operations.md](operations.md)。Go 工具链升级到 1.27.4，更新了 pgx、x/net、x/text；`govulncheck` 可达漏洞从 17 项降到 0，生产前端 `npm audit` 为 0 项。镜像本地 Buildx 构建成功，生成含 38 个包的 SPDX SBOM 和 SLSA 来源证明。发布工作流已配置摘要签名，但尚未执行发布。`make test`、`make race` 已通过。

浏览器复查：隔离部署以管理员登录，桌面及 390px 窄屏查看挖掘总览，窄屏 `scrollWidth=390`、无整页横向溢出，控制台 0 错误/警告。截图：[桌面](qa/phase7-release-dashboard-desktop.png)、[窄屏](qa/phase7-release-dashboard-mobile.png)。

性能预检：隔离数据库的 `TestPhase7CorePerformance` 经真实 HTTP Handler 测量 10 万资产过滤列表、单任务 1 万探索节点的图与覆盖、50 个活跃 Worker 的暂停加恢复控制 API，以及 1 万事件 SSE 断线续传。当前开发机容器（10 vCPU、约 8 GB 内存）最近一次 P95 分别为 2.30 ms、23.25 ms、7.41 ms（控制 API 统计一次暂停加一次恢复，因此比单次请求更保守）；SSE 首批 500 条 6.78 ms，两次连接逐条核对 1 万个不重复事件。原 SSE 每 100 条等待 2 秒，现有积压时立即连续分页，空闲时才轮询。正式门槛仍需在规格指定的 8 vCPU/16 GB 同机 PostgreSQL 环境执行 `TIANFA_PERF_DATABASE_URL=... make perf-core`；1 GB 普通流量清理与 Evidence 读取的并行性能场景尚未建立基线。

待通过：六个完整 E2E、第 17.3 节剩余故障注入、参考环境性能基线、完整可访问性与安全审计、镜像发布工作流实跑。自动 Retester、非 HTTP 证据等已有功能缺口也需按原需求补齐。

功能边界仍见 Phase 4–6 验收记录。自动 Retester、非 HTTP 证据等缺口需要单独补齐，不能以 Phase 7 测试代替实现。
