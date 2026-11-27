# Phase 7：加固与发布计划

状态：进行中。不得将本阶段第一批质量门禁视为发布完成。

## 7A：产品定位与基础质量门禁

- 统一漏洞挖掘口径和前端任务、线索、证据、复测入口；待复核与已确认结果分开计数。
- 总览只查询优先级最高的 5 条待复核线索，并为组织内状态统计和线索队列建立索引。
- 工作台加入键盘跳转主要内容入口，表单保留可见焦点；复查桌面与窄屏。
- CI 加入 `go vet ./...` 和 `go test -race ./...`，使用独立测试 PostgreSQL。

已验证：`make test`、`make build`、`make vet`、带 PostgreSQL 的 `make race`；桌面与 390px 窄屏无横向溢出，Tab→Enter 可跳到主要内容，浏览器控制台无错误。截图：[桌面](qa/phase7-mining-dashboard-desktop.png)、[窄屏](qa/phase7-mining-dashboard-mobile.png)。

## 7B：完整端到端与故障注入

- 在受控授权靶场执行规格 E2E-01 至 E2E-06，覆盖完整自主挖掘、主备模型、范围阻断、证据存续、崩溃恢复与 Skill/MCP。
- 补齐第 17.3 节尚未覆盖的事务失败、数据库中断和控制命令竞态。

## 7C：发布加固

- 性能基线、可观测指标、安全与可访问性审计。
- PostgreSQL、Blob、Workspace、Skills、Secret 材料的一致性备份及恢复演练。
- SBOM、镜像签名、部署与运维文档，以及最终 Definition of Done 复核。

功能边界仍见 Phase 4–6 验收记录。自动 Retester、非 HTTP 证据等缺口需要单独补齐，不能以 Phase 7 测试代替实现。
