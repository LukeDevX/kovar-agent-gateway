# Kovar Agent Gateway

Go + PostgreSQL 实现的 EVM Agent 身份网关。将钱包签名认证、Gateway 白名单、Kovar 用户与专属模型 Key、预算检查及任务审计串联起来。运行使用 Go 1.26、PostgreSQL 17；不需要 Redis、MQ 或 Kovar 管理员权限。

## 核心关系与钱包安全

- `agent_id = EVM wallet address`。数据库以小写 `0x` + 40 位十六进制地址作为 Agent 主键，不另外生成 Agent UUID。
- 一个 Kovar User 可以拥有多个 Agent。
- 一个 Agent 只能绑定一个 Kovar User；不同用户的重新绑定会被拒绝。
- 一个 Agent 独占一个 Kovar API Key。
- EVM Private Key 只存在 Agent 本地。Gateway 不接收、保存或打印该私钥。
- Gateway 不托管 EVM 钱包。Agent Wallet 可以使用自己的链上资金，通过本地签名转 ETH、ERC20、Approve 或调用合约。
- Kovar API Key 用于模型调用，不是 Agent 身份凭证。
- Gateway 当前不实现资金冻结，不伪造 settlement，不修改 Kovar 用户余额。
- 钱包资金与 Kovar quota 是独立资产体系。Gateway 可创建 Axone 充值订单并查询 Kovar 订单状态；不自动转出钱包资金，也不自行增加账户 quota。

## 架构与目录

```text
Agent 本地钱包 --EIP-191--> identity（认证、管理员、白名单、预算政策）
                                  |
                                  v
                         binding（用户、专属 Key、账户）
                                  |
                                  v
                    task（路由、估算、预算检查、任务、usage）
                           /                         
                   KovarManageClient          KovarModelClient
                       /api/*                     /v1/*
```

`internal/identity`、`internal/binding`、`internal/task` 分别拥有各自业务表；跨模块使用 Service 能力。`internal/app` 负责路由和必要的应用编排；`internal/platform` 提供配置、数据库、HTTP、加密与上游传输。独立 Client 不共享用户 Cookie jar，每次调用明确传入对应 Agent 的认证上下文。

## 快速开始

```bash
python3 scripts/dev-env.py
# 创建本地 .env，生成随机 DB 密码和 AES 密钥，不覆盖已有配置。
docker compose up -d --wait
make migrate-up
make build
make run
```

开发环境监听 `127.0.0.1:8080`，数据库监听 `127.0.0.1:55432`。默认开发管理员由本地初始化脚本设置为 `admin` / `Aa123456`，通过环境变量输入启动过程；业务代码使用 bcrypt 校验数据库散列。启动不会自动执行 migration，也不会重置已有管理员密码。

```bash
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/ready
```

`configs/router.json` 默认没有路由规则。网关可正常启动和管理 Agent；收费模型调用须先配置经部署方确认的价格映射，否则返回 `PRICING_NOT_CONFIGURED`。测试通过本地 mock 提供完整价格与 Kovar 响应，完全不调用生产 Kovar。

## 配置

复制 `.env.example` 或使用初始化脚本；`make` 读取 `.env`，直接执行二进制时由进程环境提供配置。

| 配置 | 含义 |
|---|---|
| `APP_ENV` | development / test / production |
| `HTTP_ADDR` | Gateway 监听地址 |
| `DATABASE_URL` | PostgreSQL DSN，勿写入日志或提交仓库 |
| `KOVAR_BASE_URL` | Manage / Model 共用地址的回退值 |
| `KOVAR_MANAGE_BASE_URL` / `KOVAR_MODEL_BASE_URL` | 可分别配置 Host |
| `KOVAR_HTTP_TIMEOUT` | 每次上游请求超时，含 SSE 总时长；默认 120s |
| `REQUEST_TIMEOUT` | 完整 Gateway 请求期限；默认 130s |
| `AGENT_NONCE_TTL` | 注册 challenge TTL；默认 5m |
| `AGENT_SIGNATURE_MAX_CLOCK_SKEW` | 请求 Unix 秒时间戳允许偏差；默认 5m |
| `GATEWAY_ADMIN_USERNAME` / `GATEWAY_ADMIN_PASSWORD` | 首次启动创建管理员所用凭证 |
| `ADMIN_TOKEN_TTL` | 管理员随机 session 的寿命；默认 8h |
| `ENCRYPTION_KEY` | base64 编码的 32 随机字节，AES-256-GCM 根密钥 |
| `DEFAULT_PER_REQUEST_LIMIT` / `DEFAULT_DAILY_LIMIT` / `DEFAULT_MONTHLY_LIMIT` | 新 Agent 的整数 quota 预算；0 表示禁止任何正额度请求 |
| `RATE_LIMIT` / `RATE_LIMIT_IP` / `RATE_LIMIT_ADMIN_LOGIN` | 每自然分钟 Agent / IP / 管理员登录请求上限 |
| `MAX_BODY_BYTES` | 请求总大小，默认 8 MiB，可配置至 32 MiB |
| `ROUTER_CONFIG` | 路由与价格映射 JSON 文件 |
| `LOG_LEVEL` | DEBUG / INFO / WARN / ERROR |
| `POSTGRES_PASSWORD` / `POSTGRES_PORT` | 本地 Docker PostgreSQL 配置 |

### 使用远程 PostgreSQL

应用和 migration 都从 `DATABASE_URL` 读取连接串。将 `.env` 中唯一的该变量设置为远程 PostgreSQL URL；不要将凭据提交到仓库或贴入日志。远程 URL 必须显式包含数据库名，例如以 `/railway` 结尾；省略时 PostgreSQL 默认连接到与用户名同名的数据库（本例为 `postgres`），而不是 Railway 创建的 `railway` 数据库。

远程数据库不需要执行 `docker compose up`；`POSTGRES_PASSWORD` 和 `POSTGRES_PORT` 仅供本地 Docker 服务使用。执行 migration 前确认该远程实例属于本项目并允许创建业务表；`make migrate-down` 会删除本项目的表和数据，不能对远程生产库随意运行。

生产模式拒绝默认管理员密码、无效 AES 密钥和非 HTTPS Kovar 地址。Gateway HTTP 放在可信 TLS 反向代理之后，或部署于受控内部网络；本版不自动颁发证书。不信任客户端 `X-Forwarded-For`，IP 限流使用连接来源地址。经代理部署时须在边缘按真实来源 IP 限流。

## EVM Signature 协议

### 注册

`POST /api/v1/agents/challenge` 请求：

```json
{"address":"0x1111111111111111111111111111111111111111"}
```

返回 lowercase `address`、随机 32 字节十六进制 `nonce`、RFC3339 `expires_at`。用返回的地址和 nonce 构造下列 UTF-8 文本，没有尾部换行：

```text
Kovar Agent Gateway

Action: Register
Address: 0x1111111111111111111111111111111111111111
Nonce: <challenge nonce>
Timestamp: <Unix seconds>
```

Agent 本地 EIP-191 / `personal_sign` 此完整文本；发送至 `POST /api/v1/agents/register`：

```json
{"address":"0x...","nonce":"...","timestamp":1700000000,"signature":"0x..."}
```

同时提供 `Idempotency-Key`。Gateway 以 Ethereum personal message prefix 加 Keccak-256 恢复 secp256k1 地址，检查低 S 签名与恢复位（0/1 或 27/28）。nonce 消费、创建 Agent／白名单／预算政策、审计及幂等结果在同一个数据库事务中提交。

注册后为 `REGISTERED/PENDING`，只证明钱包所有权。签名从未包含 EVM 私钥。可运行的本地签名示例在 `tests/gateway_test.go` 与 `internal/platform/secure/secure_test.go` 中，测试钱包通过 `crypto.GenerateKey()` 在测试进程内生成。

### 后续请求

发送 `X-Agent-Id`、`X-Timestamp`、`X-Nonce`、`X-Signature`。`X-Nonce` 由 Agent 本地生成 32 个随机字节并编码为 64 位 hex，无须再次取 challenge。签名原文精确为：

```text
Kovar Agent Gateway\nAction: Request\n
METHOD\n
REQUEST_TARGET\n
TIMESTAMP\n
NONCE\n
SHA256_HEX_BODY\n
IDEMPOTENCY_KEY
```

上面每个 `\n` 是一个实际换行；用字符串拼接表达即：

```go
message := "Kovar Agent Gateway\nAction: Request\n" +
    method + "\n" + requestTarget + "\n" + timestamp + "\n" + nonce + "\n" +
    sha256Hex(exactBodyBytes) + "\n" + idempotencyKey
```

- METHOD 是实际大写 HTTP method。
- REQUEST_TARGET 是 escaped path 加原始 query，例如 `/api/v1/tasks?page=1&page_size=20`；不能重排 query。
- SHA256 是实际发送 body 字节的 lowercase hex，空 body 同样要计算 SHA256。
- 无 `Idempotency-Key` 时最后一段为空，因此文本以 body hash 后的换行结束。
- multipart 请求签名整个编码后的 body（含 boundary），每次重试幂等请求保持相同 body 字节，但重新生成 timestamp、nonce、signature。
- 签名绑定 query 与幂等键，防止重定向查询语义或将签名复制为另一次收费操作。
- timestamp 与 nonce 均通过后，再按数据库唯一约束原子写入 nonce。撤销、暂停或未批准 Agent 均拒绝。

所有 Agent API 都要求 `ACTIVE/APPROVED`，包括 `agents/me`。无公开接口接收钱包私钥。

## Gateway Admin 与白名单

管理员使用 `POST /api/v1/admin/login` 返回的随机 token，随后 `Authorization: Bearer <gateway_admin_token>`；数据库只存 token 的 SHA256。该 token 不能替代 Agent 签名，也不转发给 Kovar。

| 操作 | 合法来源 | 结果 |
|---|---|---|
| approve | REGISTERED + PENDING/REJECTED | ACTIVE / APPROVED |
| reject | REGISTERED + PENDING | REGISTERED / REJECTED |
| suspend | ACTIVE / APPROVED | SUSPENDED / APPROVED |
| resume | SUSPENDED / APPROVED | ACTIVE / APPROVED |
| revoke | 任意 Agent 状态 | REVOKED / REVOKED，不能恢复 |

管理 API 接受可选 `{"remark":"..."}`，记录 reviewer、时间、remark 及审计。Suspend 保留用户和 Key 绑定。Revoke 先提交本地失效，再请求真实的 `DELETE /api/token/{id}`；失败返回 `token_deletion_pending`，本地权限仍立即失效。管理员重复 revoke 可重试删除；未确认删除的 Key 不允许重新创建。

管理员列表通过批量查询拼接用户、Key、预算与任务数据；详情尝试刷新上游 Token 信息，失败显示 `token_refresh_error`，不隐藏 Agent 状态。所有这些 API 都不返回原始模型 Key、Kovar Cookie 或密码。

## Kovar 用户绑定与专属 Key

批准后调用 Gateway Kovar register/login/2fa。Kovar 密码只存在当前请求，既不保存到数据库也不进入审计。登录返回的 Session Cookie 经 AES-GCM 加密持久化，用于用户 self、Token 管理等用户授权操作；不使用 Kovar Admin。

登录响应使用文档 `ApiResponse.data` 中可验证的 `User.id`，随后调用 `/api/user/self` 校验身份。若仅返回 Session、没有完整身份，保存有效期 5 分钟的加密登录上下文，返回 `authentication_complete:false, continuation_required:true`，可继续 `/kovar/auth/login/2fa`。不臆造上游 `requires_2fa` 字段。

`POST /api/v1/bindings` 也支持现有管理 Access Token：

```json
{"kovar_user_id":7,"access_token":"YOUR_MANAGEMENT_ACCESS_TOKEN"}
```

这里的用户 ID 只是校验提示，必须由带对应凭证的上游 self 响应确认；不能凭 ID 直接绑定。相同用户可刷新 Session，不同用户 rebind 被拒绝。

绑定后 `POST /api/v1/agent/token`，body `{}` 或：

```json
{"remain_quota":1000,"expired_time":2000000000}
```

默认额度取该 Agent 月预算，默认有效期 30 天。Gateway 创建 `agent-{lowercase_address}`；本地先占据唯一创建槽位，再调用上游，不盲目重试。数据库 `agent_address UNIQUE`、`kovar_token_id UNIQUE`、规范化 Key 指纹 `UNIQUE` 防止一个 Agent 多 Key 和多个 Agent 共用 Key。AES-GCM 的 AAD 绑定 Agent 地址与用途，不能把其他 Agent 的密文移植过来。

创建响应若未包含 Token，则通过 `/api/token/search?keyword=agent-...&p=1&page_size=100` 精确识别。新版搜索和详情的 Key 已脱敏；校验用户归属及 Agent token name 后，通过 `POST /api/token/{id}/key` 获取完整 Key。没有结果、多个结果、搜索结果不完整或 Key 获取失败时进入显式 reconciliation 状态，绝不重新创建另一个收费凭证。

## Kovar Manage API

当前事实来源为 [`new-kovar-manage-api.json`](new-kovar-manage-api.json)，旧版 `kovar-manage-api.json` 保留用于比较。Client 覆盖注册、登录、2FA、self、Token create/search/get/key/delete、`/api/usage/token/`、pricing、ratio config、data self、用户模型、topup info/history/status、Axone chains/order、Axone wallets、Axone PayGo sessions（创建/列表/详情/关闭）、个人 logs/stat、个人 Kovar task 查询。完整差异和补充源码依据见 [迁移矩阵](docs/manage-api-migration.md)。

管理认证为 Session Cookie 或管理 Access Token，并携带 `New-Api-User`。模型 Token usage 使用 `Authorization`，不使用 `/api/log/token?key=`。

充值先查询 info，再由用户明确选择 provider。`POST /api/v1/account/topup` 沿用 `provider/payload` 和 `Idempotency-Key`，新增 `provider:"axone"`；payload 为 `amount`（正整数）、`currency`、`chain_id`、`payment_wallet_address`。可通过 `GET /api/v1/account/topup/axone/chains` 查询链信息。创建返回 `pending` 订单与支付地址，不代表支付成功；使用 `GET /api/v1/account/topup/status?trade_no=...` 查询绑定用户的订单。上游状态原样保留（当前源码为 pending/success/failed/expired），quota 仍从 account 查询，不在 Gateway 入账。epay、stripe、creem 尚未接入，继续返回 `NOT_SUPPORTED`。历史支持 `page/page_size/keyword`，映射上游分页并保留真实 total。

pricing 始终携带绑定用户认证，以兼容 HeaderNavModules/requireAuth。管理上游 400/401/403/404/409/429 映射为对应 Gateway 状态和固定错误码；5xx 映射 502，超时 504。HTTP 200 的 `success:false` 或 `message:"error"` 仍视为失败；上游 message 和认证信息不直接返回。

## Kovar Model API 与 Task

事实来源为 [`kovar-new-api.json`](kovar-new-api.json)。Gateway `GET /api/v1/models` 使用绑定用户的 `/api/user/models`，保持 `{data:[{id}]}` 响应。任务执行仍使用 Agent 专属 Key 的 `GET /v1/models` 检查 Key 可用模型，再调用 Model API；不使用 Dashboard 或管理员模型列表。

创建任务：

```json
{
  "task_type":"chat",
  "model":"your-available-model",
  "payload":{
    "messages":[{"role":"user","content":"Hello"}],
    "max_completion_tokens":128,
    "stream":false
  }
}
```

`model` 可省略，按配置规则顺序选择可用且预算允许的模型。`payload.model` 也可使用，但两处同时提供时必须相同。`tools`、`tool_choice`、`response_format` 等放在 `payload` 内，按源文档 schema 校验。

| task_type / 选项 | 上游路径 | 编码 |
|---|---|---|
| chat | POST `/v1/chat/completions` | JSON / SSE |
| image，默认 protocol=openai | POST `/v1/images/generations/` | JSON，`prompt` 等 |
| image + operation=edit + openai | POST `/v1/images/edits/` | multipart，image / mask |
| image + protocol=qwen | POST `/v1/images/generations` | JSON，`input.messages` |
| image + protocol=qwen + operation=edit | POST `/v1/images/edits` | JSON，`input.messages` |
| video | POST `/v1/video/generations` | JSON |
| video 查询 | GET `/v1/video/generations/{task_id}` | JSON |
| speech | POST `/v1/audio/speech` | JSON 请求 / 音频响应 |
| transcription | POST `/v1/audio/transcriptions` | multipart |
| embedding | POST `/v1/embeddings` | JSON |
| rerank | POST `/v1/rerank` | JSON |

OpenAI 与 Qwen 图像接口的尾部斜杠是原文档的协议区别，Client 保留。未知 task_type／协议返回 `NOT_SUPPORTED`，不存在任意 URL 的上游代理。

文件任务用 Gateway multipart：`request` part 是上面的任务 JSON，另加 `image`、`mask` 或 `file` part。文件在有界内存中处理，不写入本地文件系统。OpenAI 图像编辑要求小于 4 MiB、边长不超过 4096 的方形 PNG，mask 尺寸必须与原图一致。speech 结果在 Task 中以 `content_type`、`audio_base64` 返回；transcription 支持 JSON、text、srt、vtt 等文档格式。

`CREATED → RUNNING → SUCCEEDED/FAILED/CANCELLED`。创建 Task 和持久化幂等映射先于模型 POST；每个 `(agent_id,idempotency_key)` 只执行一次模型任务。重放返回现有 Task，不重发模型调用，也不重放 SSE 字节。异步 video 返回 RUNNING 和 provider_task_id，之后 GET Gateway Task 通过真实视频查询路径刷新状态；查询失败保留 RUNNING，不伪造最终结果。

对于 `stream:true`，响应为 SSE，提供 `X-Task-Id`；逐行转发并 flush，不等完整结果。关闭客户端连接会取消上游。流结束缺少 `[DONE]` 会记为失败；中途错误使用 `gateway_error` SSE 事件。正常 response usage 或 SSE usage 更新 token 统计；上游没有 usage 时字段保持 0。任务已经创建后，上游失败以 HTTP 200 的 `Task.status=FAILED` 返回；创建前的认证、输入、预算错误使用统一错误响应。

## Pricing / Model Router / Budget Guard

预算和所有成本均为 **整数 Kovar quota 单位**，没有 float 金额计算。所有估算使用 `big.Rat` 精确计算后向上取整。

源文档未说明价格表形状、模型倍率、输出倍率或货币 → quota 换算。配置必须由部署方根据实际 `/api/pricing` 响应确认，JSON pointer 相对于 `ApiResponse.data`。以下为**测试 fixture 格式，绝不是声称真实 Kovar 存在这些字段或价格**：

```json
{
  "rules":[{
    "task_type":"chat",
    "model":"fixture-chat",
    "pricing_pointer":"/fixture-chat",
    "quota_multiplier":"1",
    "unit":"request",
    "max_input_bytes":8192,
    "max_output_tokens":128
  }]
}
```

测试 mock 的 data 为 `{"fixture-chat":"2"}`，估算 2 quota。生产配置须替换模型、pointer、价格单位及 multiplier，重启后生效。supported `unit`: request、token、image、second、character；估算乘以 n（包括 Qwen parameters.n）。`token` 以输入字节数保守估算文本 tokens，加输出 cap；不用于图像、音频或 multimodal chat。分开的输入／输出价格应配置经过确认的保守上界价格，不能把未说明的字段套用成真实价格。

每次创建的检查顺序：

1. 获取 Agent 当前用户 self、Token usage、Token 明确的余额字段和 pricing。
2. 选择 `/v1/models` 中实际可用的模型；指定但不可用返回 `MODEL_NOT_AVAILABLE`。
3. 检查账户可用 quota、Token remain_quota／expiry、per-request／daily／monthly 预算。
4. 在短事务中锁定该 Agent，重新检查本地周期累计，提交 Task，再调用模型。

账户 API 输出 quota、used_quota、available_quota、request_count。由于文档不解释 quota 的语义，目前依用户需求以 `max(quota-used_quota,0)` 计算 available_quota；部署前须确认 Kovar 的实际字段语义。

UTC 日/月窗口累计采用：已知 actual_cost 时用 actual，否则使用 estimated_cost，包含执行中与失败／不确定任务的估算。该值是网关的保守预算统计，不是账户余额扣减或资金冻结。不会把 estimated_cost 写入 actual_cost。

两份文档没有可证实的每请求扣费金额或精确结算关联，本版 `actual_cost` 始终为 NULL。保留字段供有依据的协议补全后接入；响应 token usage 优先于 Token 总量差分。`/api/usage/token/` 返回形状缺失，所以按不透明数据查询，不猜额度字段；预算的 Key 余额来自文档定义的 Token.remain_quota。

## Database / Docker / Migration

迁移创建需求中的 11 张业务表：`gateway_admins`、`agents`、`agent_challenges`、`agent_whitelist`、`agent_user_bindings`、`agent_kovar_tokens`、`agent_budget_policies`、`gateway_tasks`、`agent_usage_records`、`idempotency_records`、`audit_logs`。

此外有 `admin_sessions`、`pending_kovar_logins`、`rate_limit_windows` 和迁移版本表。包含 PK/FK/UNIQUE/CHECK、按 Agent、用户、Token、状态、时间和幂等键的索引。

```bash
make migrate-up       # 可重复执行，版本表跟踪
make migrate-down     # 删除本项目业务表和数据；仅用于明确需要的回滚
make docker-up
make docker-down      # 保留命名卷
```

Migration 使用事务和 PostgreSQL advisory transaction lock。应用没有 AutoMigrate。连接池显式限制为 20 个连接、5 个空闲连接。SIGTERM 时取消请求并 graceful shutdown。每分钟清理过期 nonce、admin session、pending login 和限流窗口；任务幂等记录不自动删除，避免历史请求再次收费。

Dockerfile 为多阶段构建、非 root 运行，运行层包含 CA 证书和二进制，不包含源码或 `.env`；显式 `--env-file` 注入配置。容器对外服务时将 `HTTP_ADDR` 配置成 `:8080`。

Gateway、Admin Webside、PostgreSQL、Nginx 和 HTTPS 的 AWS EC2 完整部署步骤见
[`deploy/README.md`](deploy/README.md)。

## Testing

```bash
go fmt ./...
go vet ./...
go test ./...          # unit/client 测试；未提供 TEST_DATABASE_URL 时明确 skip DB 测试
go build ./...
make lint
make test-integration  # 使用 .env 的数据库，含 go test -race ./...
```

数据库测试每次建立随机独立 schema 并清理，不操作业务 schema。所有 Kovar 测试通过 `httptest.Server`，不访问生产 Kovar。覆盖完整注册→批准→登录绑定→创建独占 Key→模型可用性→预算→chat→usage 流程，以及 revoke 后 403 且模型调用次数不变。

额外覆盖 nonce 重放／失效、伪签名／地址／时间戳、管理员登录／过期、状态转换、不同用户重新绑定、并发任务幂等、并发预算、数据库 Key 唯一约束、AES-GCM 防篡改、Client 401/429/500/timeout/malformed JSON、SSE usage 和断连取消、图像/视频/语音/嵌入/rerank。

请求 schema 可用 `python3 scripts/generate-model-schemas.py` 从源文档再生成。完整验证结果见 [docs/verification.md](docs/verification.md)，路由清单见 [docs/gateway-api.md](docs/gateway-api.md)。

## Security 与运维边界

- 管理员认证与 Agent 鉴权独立；撤销与任务准入通过同一 Agent 行锁序列化。
- 敏感请求幂等 hash 使用 keyed HMAC，避免以普通密码 hash 形式持久化 Kovar 密码；body 不持久化。
- Task.request_payload 只保存 hash、大小、协议等元数据，避免保存用户 prompt/文件/潜在敏感内容；模型结果及 usage 会保存，需设定符合业务的备份与保留政策。
- Raw Kovar Key 和管理 Session 经 AES-GCM 加密；管理员密码 bcrypt；管理 session token 只存 SHA256。更换 ENCRYPTION_KEY 前必须迁移已有密文，不能直接更换环境变量后继续使用旧数据。
- 日志只记录 request_id、路由、method、status、耗时和固定错误码，不记录 body、headers、Cookie、密码、完整签名或 Key。Audit 接口只接受固定结果码；不接收任意 metadata。
- 上游响应有 32 MiB 上限，SSE 单行 1 MiB；请求和上传有界。JSON 请求根字段按文档校验，未知字段拒绝。
- 上游 HTTP 使用共享 transport、TLS 验证、超时、禁止跟随 redirect。只有 GET 的临时错误有限退避重试；POST、DELETE 不自动重试。
- 客户端取消不会重试收费调用。仅本地任务终态／usage／幂等提交使用保留请求元数据的独立 5s 有界收尾 context，不延长上游模型请求生命周期。

## Known Limitations / 文档缺口

1. 新版 Manage 多数响应仍只有 ApiResponse，data 未详细定义；用户模型、分页、充值状态和 Axone 的最小字段依据同级上游源码确认，其他字段保留可脱敏的 raw JSON。实际部署不符合时返回 `KOVAR_CONTRACT_INCOMPLETE`，没有把 mock 当生产验证。
2. pricing／ratio config／Token usage／data self／日志统计字段缺失。默认路由为空且收费请求 fail closed；price pointer 和 quota 换算须由部署方确认。实际费用无法确认时永远 NULL。
3. epay、stripe、creem 新版已有请求 schema，但其 provider adapter 本次未接入，继续返回 NOT_SUPPORTED；Axone address 别名和支付回调暂不公开；Axone wallets 与 PayGo 会话已通过 /api/v1/account/axone/wallets、/api/v1/account/paygo/sessions 系列公开。Gateway 不执行钱包转账或自行结算。
4. 完整模型 Key 通过新版 key endpoint 获取，失败进入 reconciliation 状态。网络超时、进程崩溃或上游成功而本地写入失败，可能留下 CREATING/UNKNOWN 或 RUNNING；需用已记录 Agent/Token name/provider_task_id 在 Kovar 核实后处理。充值结果不确定时也必须核实订单，禁止通过清除幂等记录盲目重发收费请求。
5. 预算是预检查。多个 Agent 共用同一 Kovar 用户、外部使用账户、实际费用超估算及上游未知扣费会影响真实余额；Kovar 执行最终计费。Gateway 不保证上游原子余额预留。
6. Suspend/Revoke 阻止新准入；已提交给上游的请求不能通过未记载的取消接口撤销。视频以 GET Task 按需轮询，无后台自动重试收费请求。
7. 本版不实现外部支付回调、Kovar 管理员接口、Kling/Jimeng/Sora 专用协议、Responses、Passkey 或非 EVM 身份；这些不属于第一版所需最小调用能力。
8. 生产上线仍需真实 Kovar 合约联调、价格单位确认、TLS/边缘限流、备份、密钥轮换与任务结果保留设置；本仓库的验证范围是本地 PostgreSQL 和受控 mock 上游。
