# 验证记录

2026-09-17，在当前 macOS arm64 / Go 1.26.1 / Docker Desktop 环境完成。

| 检查 | 结果 |
|---|---|
| `go fmt ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test ./...`，提供 TEST_DATABASE_URL | 通过，包含真实 PostgreSQL 集成测试 |
| `go build ./...` | 通过 |
| `make lint` | 通过 |
| `make test-integration` (`go test -race ./...`) | 通过 |
| `make build` | gateway / migrate 二进制成功 |
| `docker compose config --quiet` | 通过 |
| `docker compose up -d --wait` | PostgreSQL healthy |
| `make migrate-up` | 成功，重复执行安全 |
| Migration up/down/up | 独立测试 schema 往返通过，恢复 15 张表（含版本表） |
| `docker build -t kovar-agent-gateway:local .` | 成功 |
| Gateway 二进制运行 | `/health`、`/ready` 200；管理员登录及 `/admin/me` 成功 |
| SIGTERM | 优雅关闭，退出码 0 |
| Gateway Docker 容器运行 | `/ready` 200；uid=10001；只读 root filesystem |
| 本地配置密钥扫描 | 随机数据库密码／AES 根密钥未出现在交付源码、文档或构建文件 |
| 两份 Kovar 原始 JSON | SHA256 与 source-contracts.md 记录一致 |

Kovar 上游测试全部使用 httptest.Server；没有以生产 Kovar 执行登录、充值或模型调用。数据库测试使用随机 schema，测试后清理；启动 smoke test 使用本地开发数据库。

验证包括并发幂等任务只调用一次模型、并发预算只允许剩余额度内的任务、用户 1:N Agent、数据库拒绝共用 Key 指纹、撤销后 403 且模型调用数不增加、Token 删除失败仍保持撤销、SSE 首字节即时返回及客户端断连取消、请求及响应大小限制、请求签名绑定 query/body/幂等键、未定义充值协议拒绝执行。

本机未安装 golangci-lint；项目 lint target 使用实际执行成功的 go vet。生产 Kovar 契约联调和价格／额度语义确认不在这份本地验证结果中，具体缺口见 README。
