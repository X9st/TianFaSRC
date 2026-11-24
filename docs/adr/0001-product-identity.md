# ADR-0001：产品身份与 Phase 0 技术边界

- 状态：采纳
- 日期：2026-11-24

## 决策

使用用户仓库上下文提供的独立标识：产品名“天伐 / TianFa”，Go module
`github.com/X9st/TianFaSRC`，二进制 `tianfa-server`，Compose 服务 `app/postgres`，
环境变量前缀 `TIANFA_`，数据库名 `tianfa`。

后端采用 Go 模块化单体；前端采用 Next.js 静态导出，并在生产构建中复制到 Go
包内通过 `go:embed` 提供。同源部署减少认证、CSRF 和 SSE 的跨域复杂度。

## 权衡

- Phase 0 只实现真实基础设施状态，不伪造任务、Finding 或 Agent 数据。
- PostgreSQL 未配置时服务允许启动以便诊断，但 readiness 返回 503；生产 Compose
  始终配置数据库。
- 流量代理地址现在进入配置但不启动监听，真实代理属于 Phase 5。

## 等价保障

OpenAPI、配置测试、健康检查测试、迁移锁和 Compose 健康依赖共同保证骨架契约；
后续任何标识或接口调整必须同步更新配置、文档、镜像和 API。

