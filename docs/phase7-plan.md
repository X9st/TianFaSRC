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

已有自动化证据：`TestConcurrentClaimsRecoveryAndFencing`（20 Worker 单 Lease）、`TestPlanIntentFactAndQuotaFailover`（真实 HTTP Stub 的主备切换）、`TestPartialStreamContinuesOnBackup`、`TestToolCallIdempotencyScopeAndAudit`、`TestMCPDeferredUntilSkillLoad`、`TestEvidenceSurvivesTrafficDeletionAndRejectsTampering`、`TestKilledWorkerProcessRecovers`，以及新增的 `TestFailedTrafficTransactionLeavesCollectableOrphans`、`TestPauseCancelAndReconcileRaceHasOneTerminalReason`。并发测试发现 Reconciliation 曾读取过时的任务状态，导致取消后 Intent 重开；现对任务与 Lease 同时加行锁，重复 20 轮竞态测试通过。这些是场景的组成部分，尚不足以替代六个从 UI 到数据和报告的完整 E2E。

## 7C：发布加固

- 性能基线、可观测指标、安全与可访问性审计。
- PostgreSQL、Blob、Workspace、Skills、Secret 材料的一致性备份及恢复演练。
- SBOM、镜像签名、部署与运维文档，以及最终 Definition of Done 复核。

已落地：Compose 冷备份脚本、受限归档解包、隔离恢复演练、Blob 文件哈希/大小/引用计数及凭据解密校验；恢复后服务 ready。2026-11-27 在隔离项目中用 1 条流量 Blob 引用、1 条加密凭据和工作区文件实际演练通过，结果 `blobs_verified=1`、`traffic_refs=1`、`secrets=1`。归档安全测试进入 CI，运维步骤见 [operations.md](operations.md)。Go 工具链升级到 1.27.4，更新了 pgx、x/net、x/text；`govulncheck` 可达漏洞从 17 项降到 0，生产前端 `npm audit` 为 0 项。镜像本地 Buildx 构建成功，生成含 38 个包的 SPDX SBOM 和 SLSA 来源证明。发布工作流已配置摘要签名，但尚未执行发布。`make test`、`make race` 已通过。

浏览器复查：隔离部署以管理员登录，桌面及 390px 窄屏查看挖掘总览，窄屏 `scrollWidth=390`、无整页横向溢出，控制台 0 错误/警告。截图：[桌面](qa/phase7-release-dashboard-desktop.png)、[窄屏](qa/phase7-release-dashboard-mobile.png)。

待通过：六个完整 E2E、第 17.3 节剩余故障注入、参考环境性能基线、完整可访问性与安全审计、镜像发布工作流实跑。自动 Retester、非 HTTP 证据等已有功能缺口也需按原需求补齐。

功能边界仍见 Phase 4–6 验收记录。自动 Retester、非 HTTP 证据等缺口需要单独补齐，不能以 Phase 7 测试代替实现。
