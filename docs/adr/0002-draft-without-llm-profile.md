# ADR-0002：Phase 1 草稿任务的模型链

- 状态：采纳
- 日期：2026-11-24

规格的创建任务对象要求非空 `llm_profile_chain`，但 LLM Profile 与 Secret Store 属于 Phase 2；Phase 1 验收又要求 UI/API 能创建任务。Phase 1 因此允许 `draft` 任务的模型链为空，不允许启动。启动请求返回明确的依赖不可用错误，直到 Phase 2 引入可用 Profile 后才满足完整创建契约。

该决策不创建虚假的模型 Profile，不把任务标记为已运行；Phase 2 必须使新任务创建满足非空链，并提供草稿补齐入口及相应迁移和测试。
