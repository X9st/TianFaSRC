# 天罚 TianFa

面向明确授权安全团队的自主安全验证平台。项目严格以
[`全新自主安全验证平台-核心能力实现规格.md`](./全新自主安全验证平台-核心能力实现规格.md)
为产品事实来源，按 Phase 逐步实现。

当前交付：**Phase 0 — 骨架与契约**。

## 本地启动

完整环境需要 Docker Desktop：

```bash
docker compose up --build
```

打开 <http://localhost:8080>。健康检查：

```bash
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
```

如果 `8080` 已被 Burp 等本地工具占用，可以只更改宿主机端口：

```bash
TIANFA_HOST_PORT=18080 docker compose up --build
```

此时打开 <http://localhost:18080>；容器内部和应用默认端口保持不变。

本地开发也可以分别启动前后端：

```bash
make dev-server
make dev-web
```

后端默认监听 `:8080`，前端开发服务器监听 `:3000`，通过开发模式转发健康检查。
后端使用其他地址时，设置 `TIANFA_DEV_API_ORIGIN`，例如
`TIANFA_DEV_API_ORIGIN=http://127.0.0.1:18080 make dev-web`。
未配置 PostgreSQL 时后端仍可存活，但 `/health/ready` 会返回未就绪。

## 验证

```bash
make test
make build
```

OpenAPI 由 Go 路由常量和契约定义生成：`make openapi` 更新文档，
`make openapi-check` 检查文档与源码一致。PostgreSQL 迁移集成测试需要
`TIANFA_TEST_DATABASE_URL`；CI 中默认运行。

需求追踪和阶段计划见 [docs/requirements-traceability.md](docs/requirements-traceability.md)
与 [docs/implementation-plan.md](docs/implementation-plan.md)。

