# Phase 1 需求与验收

目标：用户登录后在 UI 创建授权任务，写入任务范围和双图根节点，能够查询任务、资产、血缘与覆盖；数据重启后保留。

| 需求 | 实现 | 验收 |
| --- | --- | --- |
| REQ-PROD-001、REQ-STATE-001 | Task 聚合、Goal/Constraint/Scope、受管状态命令 | UI/API 创建草稿，非法转换 409 |
| REQ-ASSET-001..005 | 组织级资产、服务端规范化与去重、任务引用 | 重复写入只保留一条，跨任务共享 |
| REQ-GRAPH-001..005 | 探索节点/边/锚点、血缘和覆盖查询 | 根 Goal 可追溯；写入事务一致 |
| REQ-DATA-001..003 | Outbox、消费者去重、Activity 投影 | 事务内事件；重复消费不重复投影 |
| REQ-DEPLOY-004 | Phase 1 内嵌迁移 | 重复和并发迁移测试 |
| 12.5 认证/RBAC | 初始化 Admin、Argon2id、Session Cookie、CSRF、角色检查 | 未认证 401、无权 403、变更请求 CSRF 拒绝 |
| 12.1 API 契约 | OpenAPI、请求验证、版本、分页与错误 | API 与生成文档一致 |

实现位置：`internal/auth`、`internal/task`、`internal/api/phase1.go`、`internal/store/postgres/migrations/000002_phase1.sql` 与 `web/app/{login,tasks,assets,users}`。

验收命令：

```bash
TIANFA_TEST_DATABASE_URL='postgres://…/tianfa_ci?sslmode=disable' make test
make build
```

`TestPhase1Flow` 使用独立 PostgreSQL 数据库验证管理员初始化、登录、CSRF、权限、创建任务、目标和约束追加、范围版本替换、乐观锁、资产去重、任意节点锚定、覆盖度、Activity 消费者去重、用户停用后的会话撤销。浏览器实测管理员登录与 UI 创建任务，详情能查看根事实、目标血缘和覆盖；桌面与 390px 手机截图见 `docs/qa/phase1-*.png`。

边界：Phase 1 没有 LLM Profile、Planner、Worker 和真实工具执行。启动请求会返回明确的 `409 model-unavailable`，并保持任务为草稿；这是 [ADR-0002](adr/0002-draft-without-llm-profile.md) 的阶段衔接。图页面以可读的节点来源列表呈现，交互式大图和 SSE 属于 Phase 6。

Phase 2 的 LLM Profile 依赖由 ADR-0002 明确处理。
