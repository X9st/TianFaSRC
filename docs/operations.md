# 天罚部署与运维

## 部署前提

- 单实例 Compose 部署需要 Docker Compose、可用 PostgreSQL 16 镜像和持久化数据盘；`app`、`postgres` 的健康状态可用 `docker compose ps` 检查。
- 在受信任的 HTTPS 反向代理后提供控制台，只让授权操作者访问。录制代理宿主机端口只绑定本机回环地址。
- 使用 `TIANFA_HOST_PORT`、`TIANFA_PROXY_HOST_PORT` 指定宿主机端口。使用 `TIANFA_DATA_HOST_PATH`、`TIANFA_SKILLS_HOST_PATH` 指定持久目录；后续备份与恢复须使用相同值。
- `POSTGRES_PASSWORD` 当前由 Compose 文件给出，生产部署须改为独立高强度凭据并同步修改 `TIANFA_DATABASE_URL`。妥善保护 Compose 配置、数据库卷和备份目录。
- 监控 `GET /health/live` 与 `GET /health/ready`。后者同时检查数据库、Blob 存储和工作区；异常时查看 `docker compose logs app postgres`。服务日志为 JSON，包含请求 ID。

## 备份

在仓库根目录执行：

```bash
./scripts/backup.sh /secure/backups/tianfa-YYYYMMDD
./scripts/restore-drill.sh /secure/backups/tianfa-YYYYMMDD
```

脚本要求 Docker Compose、Python 3、`shasum`、`curl` 和 Go（恢复演练会从当前源码构建）。备份会停止 `app`，对 PostgreSQL 做自定义格式转储，并归档数据目录与 Skill 目录，生成 SHA-256 清单后恢复 `app`。它适用于只有本 Compose 应用写库的单实例部署；如有其他写入者，须先停止它们。备份期间控制台暂不可用。使用专用受限目录存放备份，目录中有数据库、API Key 加密密钥和任务资料。建议在每次升级前、至少每天备份，并定期把副本移到独立存储。

每次备份至少保留 `database.dump`、`data.tar`、`skills.tar`、`manifest.sha256` 四个文件。演练先检查归档哈希，再在临时 PostgreSQL 中恢复数据库和文件，校验每个有引用的 Blob 的大小、SHA-256、流量与证据引用计数，逐条解密凭据，最后以只绑定回环地址的 `serve-readiness` 模式启动应用检查 ready。该模式不启动 Worker 调度、事件投递或 Blob 清理，避免克隆任务重新访问目标。结果中的 `orphan_rows` 表示尚待宽限期清理的无引用 Blob 行，不属于损坏。

## 灾难恢复到新的空部署

先运行上述恢复演练。以下步骤会替换目标部署，必须确认目标是新的空数据库与空数据目录，并使用与备份一致的应用版本/迁移版本。

1. 停止目标 `app` 以及所有其他写入者；准备新的空 PostgreSQL 数据库和空数据、Skill 目录。
2. 在备份目录运行 `shasum -a 256 -c manifest.sha256`。
3. `python3 scripts/backup_files.py extract /secure/backups/tianfa-YYYYMMDD/data.tar /new/data`，并以同样命令把 `skills.tar` 解压到 `/new/skills`。目标目录必须事先不存在。
4. 在目标 Compose 环境执行 `docker compose up -d postgres`，再执行 `docker compose exec -T postgres pg_restore --exit-on-error -U tianfa -d tianfa --no-owner --no-acl < /secure/backups/tianfa-YYYYMMDD/database.dump`。此步骤仅适用于空数据库。
5. Linux 宿主机将 `/new/data` 和 `/new/skills` 的文件所有权交给容器中的 `tianfa` 用户（当前镜像 UID 100、GID 101），保持私钥 `0600`、目录 `0700`。将两个目录分别设置为 `TIANFA_DATA_HOST_PATH`、`TIANFA_SKILLS_HOST_PATH`。
6. 执行 `docker compose run --rm --no-deps app tianfa-server verify-data`，确认退出码 0，随后 `docker compose up -d app` 并检查 `/health/ready`、登录、任务和证据读取。

恢复失败时保持目标应用停止，保留出错日志和原始备份，修复原因后从新的空目标重试。不要让启动过程替缺失的旧 `master.key` 生成新密钥；密钥与数据库中的加密凭据必须成套恢复。

## 日常检查

- 数据库和数据目录磁盘空间、备份完成时间、演练结果、健康探针和应用错误日志。
- 高风险漏洞线索、阻断审计、模型故障转移、等待审批和长期运行的任务。
- 升级前先备份并演练；升级后运行 `/health/ready`、登录与证据读取检查。

## 镜像供应链

在主分支通过验收后，维护者可手动运行 GitHub Actions 的 `release-image` 工作流。它向 GHCR 推送以提交 SHA 标记的镜像，附加 SBOM 与构建来源证明，并以 GitHub OIDC 对镜像摘要做无密钥签名和身份验证。部署时使用该不可变摘要，核对签名身份为本仓库的 `release-image.yml` 工作流。工作流文件：[release-image.yml](../.github/workflows/release-image.yml)。
