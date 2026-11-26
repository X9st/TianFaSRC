# Phase 4 验收记录

2026-11-26，Phase 4 核心链路已接入生产 Worker：模型按结构化协议选择工具，统一 Dispatcher 校验输入、绑定、审批与授权范围，记录 ToolCall、完整输出引用、审计事件和用量，再将工具结果作为不可信观察送回模型。只有真实 HTTP 响应能生成带 ToolCall 血缘的确认 Fact。

## 本阶段实现

- `http.read` 仅允许 GET/HEAD；每次 DNS 解析和重定向都检查 Scope，连接固定到已校验地址；私网、回环、链路本地地址默认受保护。任务运行中可收紧 Scope，版本变化会丢弃在途结果。
- 工作区按任务与 Intent 隔离，文件名、大小和符号链接受限。`command.run` 仅在配置容器镜像后开放，以非 root 用户、无网络、只读文件系统和资源上限运行。
- 工具调用持久化输入、状态、策略原因、耗时、输出摘要和大输出 Blob；幂等键防止重试重复执行。`graded` 和 `approval` 模式支持人工审批与相同 ToolCall 恢复。
- Skill ZIP 验证路径、文件类型、数量和解压大小；记录版本及内容哈希，默认只给 Worker 名称和简介。`skill.load` 记录实际加载版本并解锁声明的 MCP Server。
- MCP stdio 在无网络容器中运行；Streamable HTTP 端点要求 HTTPS、固定 DNS 连接和禁止重定向。发现的工具先关闭，管理员逐项启用；Schema 哈希变化自动关闭工具。自定义 HTTP Tool 只允许 GET/HEAD，保存及执行时检查目标。
- 管理页 `/tools/` 提供内置工具开关、Skill ZIP 上传、MCP 配置与发现、自定义只读 HTTP 工具和审批队列；任务详情展示 ToolCall 与审批操作。

## 测试与验收

- PostgreSQL 集成测试覆盖 ToolCall 幂等、越界阻断与审计、审批后同 ID 恢复、Fake LLM → HTTP 工具 → 确认 Fact，以及 Fake MCP 发现、Skill deferred 解锁、Schema 与越界参数拒绝。
- 单元测试覆盖 Scope 规则、HTTP 重定向与 DNS 保护、Skill ZIP 安全校验。
- `make test`、`make build`、OpenAPI 契约检查通过；浏览器检查包含桌面和窄屏管理页及任务创建页。

视觉检查截图：[工具页桌面](qa/phase4-tools-desktop.png)、[工具页手机](qa/phase4-tools-mobile.png)、[任务创建手机](qa/phase4-task-create-mobile.png)。浏览器控制台无错误。

## 边界

Streamable HTTP MCP 的请求参数在本地校验显式 URL，但远端服务在其自身环境内发起的次级网络请求无法由本进程观测。管理员只能接入可信、受其网络出口策略约束的远端 MCP 服务；未经此条件，不应启用目标访问类远端工具。浏览器/代理流量捕获、Evidence、Finding 与报告属于 Phase 5。

本阶段移除任务预算配置，保留历史用量统计，见 [ADR-0003](adr/0003-remove-task-budgets.md)。
