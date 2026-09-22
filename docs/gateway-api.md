# Gateway API

除 `/health`、`/ready`、注册 challenge/register、管理员 login 外，Agent 路由要求 EVM 签名且 ACTIVE/APPROVED；Admin 路由要求独立 Bearer token。错误统一为 `{"code":"...","message":"...","request_id":"..."}`，每个响应返回 `X-Request-Id`。

列表默认 `page=1&page_size=20`，page_size 最大 100。管理端的名单 query `status` 为 PENDING/APPROVED/REJECTED/REVOKED。Agent ID 为 lowercase EVM address。

| Method | Path | 认证 / 请求 |
|---|---|---|
| GET | /health | public |
| GET | /ready | public，验证数据库和 migration |
| POST | /api/v1/agents/challenge | public，address |
| POST | /api/v1/agents/register | public，address/nonce/timestamp/signature；Idempotency-Key |
| GET | /api/v1/agents/me | Agent |
| POST | /api/v1/admin/login | username/password |
| GET | /api/v1/admin/me | Admin |
| GET | /api/v1/admin/agents | Admin，page/page_size |
| GET | /api/v1/admin/agents/{agent_id} | Admin |
| GET | /api/v1/admin/whitelist | Admin，page/page_size/status |
| POST | /api/v1/admin/agents/{agent_id}/approve | Admin，remark 可选 |
| POST | /api/v1/admin/agents/{agent_id}/reject | Admin，remark 可选 |
| POST | /api/v1/admin/agents/{agent_id}/suspend | Admin，remark 可选 |
| POST | /api/v1/admin/agents/{agent_id}/resume | Admin，remark 可选 |
| POST | /api/v1/admin/agents/{agent_id}/revoke | Admin，remark 可选 |
| PUT | /api/v1/admin/agents/{agent_id}/budget | Admin，per_request_limit/daily_limit/monthly_limit |
| POST | /api/v1/kovar/auth/register | Agent，文档注册字段；Idempotency-Key |
| POST | /api/v1/kovar/auth/login | Agent，username/password；Idempotency-Key |
| POST | /api/v1/kovar/auth/login/2fa | Agent，code；Idempotency-Key |
| POST | /api/v1/bindings | Agent，kovar_user_id/access_token；Idempotency-Key |
| GET | /api/v1/bindings/me | Agent |
| POST | /api/v1/agent/token | Agent，{} 或 remain_quota/expired_time；Idempotency-Key |
| GET | /api/v1/agent/token | Agent，脱敏 Token 信息 |
| DELETE | /api/v1/agent/token | Agent，先停止本地 Key 使用再删除上游 |
| GET | /api/v1/account | Agent，User self 额度字段 |
| GET | /api/v1/account/topup/info | Agent |
| GET | /api/v1/account/topups | Agent，page/page_size/keyword；使用上游分页和 total |
| GET | /api/v1/account/topup/status | Agent，必填 trade_no；仅绑定用户的订单 |
| GET | /api/v1/account/topup/axone/chains | Agent，Axone 支持的链 |
| POST | /api/v1/account/topup | Agent，provider/payload；Idempotency-Key；新增 axone，其他 provider 仍 NOT_SUPPORTED |
| GET | /api/v1/account/axone/wallets | Agent，Axone 钱包列表；返回 `list[]`，其中 `id` 即 paygo 的 wallet_id |
| POST | /api/v1/account/paygo/sessions | Agent，wallet_id/max_amount；Idempotency-Key；创建按量支付会话 |
| GET | /api/v1/account/paygo/sessions | Agent，按量支付会话列表 |
| GET | /api/v1/account/paygo/sessions/{id} | Agent，id 为 session_id（UUID），不是数值主键 |
| POST | /api/v1/account/paygo/sessions/{id}/close | Agent，Idempotency-Key；关闭按量支付会话 |
| GET | /api/v1/models | Agent 绑定用户的可用模型，响应仍为 `{data:[{id}]}` |
| GET | /api/v1/pricing | Agent，管理 pricing data |
| POST | /api/v1/tasks | Agent，task_type/model/payload；Idempotency-Key |
| GET | /api/v1/tasks | Agent，page/page_size |
| GET | /api/v1/tasks/{task_id} | Agent，只能读取自己的任务；video 按需刷新 |
| GET | /api/v1/usage | Agent，page/page_size，Gateway usage records |
| GET | /api/v1/logs | Agent，page/page_size，Kovar 个人日志 |
| GET | /api/v1/logs/stat | Agent，Kovar 个人统计 |

管理列表与详情上的 today_usage/month_usage 是本地预算累计：实际值存在时用实际，否则用估算；不是 Kovar 扣款凭证。Model 任务已创建后即以 Task 表达执行失败，重放请求读取现有 Task；前置拒绝则返回 4xx/5xx。

本项目不会转发任意上游 path、认证头或 query。充值历史仅发送 `p`（来自 Gateway `page`）、`page_size`、`keyword`，默认 1/20，page_size 最大 100；不再对上游返回页二次切片。trade_no、keyword 最大 256 字节，不接受控制字符。用户模型发现无需先创建模型 Key；任务执行仍要求专属 Key 和原有预算检查。

Axone 创建示例（链、币种、钱包地址由用户根据 topup info/chains 明确选择，以下仅为占位）：

```json
{"provider":"axone","payload":{"amount":10,"currency":"USDC","chain_id":"SELECTED_CHAIN","payment_wallet_address":"SELECTED_WALLET"}}
```

创建只返回 pending 订单。查询 status 时原样返回 Kovar 的状态及金额，不将 paid/completed 等状态互相转换，不本地增加 quota。相同逻辑写操作重试须保持完全相同的 body 和 Idempotency-Key，并重新签名；上游不确定失败也不会重复创建订单。

### 按量支付（Axone PayGo）调用流程

按量支付依赖“已绑定的 Kovar 用户凭证”，所有接口均为 Agent 鉴权。完整流程如下：

1. **绑定用户**（复用现有能力）：`POST /api/v1/kovar/auth/register` 或 `/login`（必要时 `/login/2fa`），把 Agent 绑定到 Kovar 用户并保存加密 Session。
2. **获取钱包**：`GET /api/v1/account/axone/wallets`，响应 `data.list[]` 中每一项的 `id` 即 `wallet_id`，同时含 `currency`、`total_balance` 等字段。
3. **创建会话**：`POST /api/v1/account/paygo/sessions`，请求体 `{"wallet_id":"...","max_amount":"1.00"}`，必须带 `Idempotency-Key`；返回 `session_id`（UUID）和 `status:"active"`。
4. **查询**：`GET /api/v1/account/paygo/sessions`（列表）或 `GET /api/v1/account/paygo/sessions/{session_id}`（详情）。
5. **关闭会话**：`POST /api/v1/account/paygo/sessions/{session_id}/close`，也必须带 `Idempotency-Key`；成功返回 `status:"closed"` 并写入 `closed_at`。

关键契约：

- `{id}` 一律使用 `session_id`（UUID），不是响应里的数值 `id`。
- 创建与关闭都要求 `Idempotency-Key`（上游 close 同样校验，缺省会返回 400）。
- `max_amount` 是十进制字符串；会话金额字段为 q8 定点整数（8 位小数），Gateway 原样透传、不本地结算。
- 常见业务错误：402 会话不可用或预留额度不足、404 会话不存在、409 幂等键冲突、503 按量支付未启用。

示例（省略 Agent 签名头）：

```text
# 1. 获取钱包，取 list[0].id 作为 wallet_id
GET /api/v1/account/axone/wallets

# 2. 创建会话
POST /api/v1/account/paygo/sessions
Idempotency-Key: paygo-create-001
{"wallet_id":"<wallet_id>","max_amount":"1.00"}

# 3. 详情（id 用返回的 session_id）
GET /api/v1/account/paygo/sessions/<session_id>

# 4. 关闭
POST /api/v1/account/paygo/sessions/<session_id>/close
Idempotency-Key: paygo-close-001
```

除上述 `GET /api/v1/account/axone/wallets` 与四个 paygo 会话接口外，按量支付不依赖其它尚未暴露的上游接口；`chains`、`topup/info` 属于预付（topup）流程，与 paygo 无关。

管理错误映射：400 `KOVAR_INVALID_REQUEST`，401 `KOVAR_AUTH_FAILED`，403 `KOVAR_FORBIDDEN`（包括 pricing 模块关闭），404 `KOVAR_NOT_FOUND`，409 `KOVAR_CONFLICT`，429 `UPSTREAM_RATE_LIMITED`，5xx → 502 `UPSTREAM_ERROR`。HTTP 200 业务失败 → 502 `KOVAR_REQUEST_REJECTED`；订单不存在或不属于当前用户 → 404。响应结构异常返回 502，不直接透出上游消息。

日志分页维持现有行为，本次没有扩展日志过滤器；其上游分页适配可单独处理。
