# Phase 5 验收记录

2026-11-26，Traffic、Evidence 与 Finding 的核心闭环已落地：真实 HTTP 工具或任务记录代理写入流量；Reviewer 将流量绑定为不可变证据并确认 Finding；报告按证据版本保存与导出。界面位于 `/findings/`，按任务切换流量、漏洞与证据、报告、记录代理。

## 实现范围

- HTTP/HTTPS 记录代理使用任务令牌和本地 CA；令牌只展示一次、保存哈希且一小时过期。代理与 `http.read` 均在连接前检查 Scope 和解析地址，固定实际连接地址，范围版本变化则拒绝入库。代理当前只放行无正文的 GET/HEAD 请求。
- 流量索引保存 Task/Run/Intent/ToolCall 血缘、时间、实际远端 IP、TLS、方法、URL、状态、Headers、原字节长度、正文长度及哈希；原请求和响应存储在内容寻址 Blob。普通列表掩码敏感 Header，Reviewer/Admin 查看原值和下载原字节会留下审计记录。支持主机、路径、状态、时间、响应正文关键词与长度筛选。
- Finding 绑定任务资产和探索图节点，按组织、首个规范化资产、漏洞类型及位置生成指纹；同任务重复上报合并来源 Run。Worker 的 `finding.report` 仅能在同一 Run 有成功真实 HTTP 观察后创建草稿，不能自行确认。可选 CVSS 3.1 vector/score 使用服务端库重新计算并校验。评分规则依据 [FIRST CVSS v3.1 规范](https://www.first.org/cvss/v3.1/specification-document)。
- 绑定流量会冻结元数据、原始 Headers 和 Blob 哈希；Evidence 有独立引用计数。删除普通流量后，Evidence 仍可校验并读取。证据绑定、用途、说明、排序或解除绑定会推进 Finding 证据版本。后台按保留期回收无引用 Blob。
- Reviewer/Admin 复核至少一份 Proof 证据后可确认漏洞。复测记录固定原 Finding 证据版本和 Scope 版本，只接受创建复测后采集的 Verification 证据。`fixed`、`still_vulnerable` 更新 Finding 状态，`inconclusive` 保留原状态。
- 任务报告保存当前证据版本并使用乐观锁；证据变化时前端显示过期。Markdown、JSON、证据 ZIP 导出前验证原字节 SHA-256，ZIP 完成后验证 CRC；证据丢失或损坏时导出失败。

## 验收证据

- `TIANFA_TEST_DATABASE_URL=... make test`：通过。集成测试覆盖 Worker 上报门槛、HTTP/HTTPS 代理和 Scope、证据在流量删除后存续、重复绑定、报告版本冲突、复测、证据排序、Blob 篡改后拒绝读取和导出。
- `make build`、`make openapi-check`：通过。
- 浏览器在本地服务中登录并创建任务、关联资产、创建 Finding 草稿、保存报告，检查四个功能页和 390px 窄屏；无横向溢出，控制台无错误。截图：[流量桌面](qa/phase5-traffic-desktop.png)、[漏洞桌面](qa/phase5-findings-desktop.png)、[报告桌面](qa/phase5-report-desktop.png)、[代理桌面](qa/phase5-proxy-desktop.png)、[漏洞手机](qa/phase5-findings-mobile.png)、[代理手机](qa/phase5-proxy-mobile.png)。

## 部署与边界

- 源码开发默认代理监听 `127.0.0.1:8081`。Compose 内部监听 `:8081`，宿主机仅映射 `127.0.0.1:8081`；其他设备需由管理员在受控网络中显式转发。客户端设置 HTTP 代理认证：用户名 `tianfa`，密码为当前任务令牌。HTTPS 客户端需仅在授权测试环境信任下载的 CA。CA 私钥位于数据目录 `ca/ca.key`，权限为 `0600`；备份时需保护该目录。
- 本阶段复测为独立、持久化的人工验证记录，尚未接入自动 Retester Agent。其证据与结论仍由受管 API 检查。代理只支持 GET/HEAD；写操作需在后续阶段设计更细的审批和 Scope 策略后开放。Phase 6 继续补全实时工作台及更完整的图形展示。
