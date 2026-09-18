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
| GET | /api/v1/account/topups | Agent，page/page_size |
| POST | /api/v1/account/topup | Agent，provider/payload；Idempotency-Key；缺少协议时 NOT_SUPPORTED |
| GET | /api/v1/models | Agent 专属 Key 的实际模型列表 |
| GET | /api/v1/pricing | Agent，管理 pricing data |
| POST | /api/v1/tasks | Agent，task_type/model/payload；Idempotency-Key |
| GET | /api/v1/tasks | Agent，page/page_size |
| GET | /api/v1/tasks/{task_id} | Agent，只能读取自己的任务；video 按需刷新 |
| GET | /api/v1/usage | Agent，page/page_size，Gateway usage records |
| GET | /api/v1/logs | Agent，page/page_size，Kovar 个人日志 |
| GET | /api/v1/logs/stat | Agent，Kovar 个人统计 |

管理列表与详情上的 today_usage/month_usage 是本地预算累计：实际值存在时用实际，否则用估算；不是 Kovar 扣款凭证。Model 任务已创建后即以 Task 表达执行失败，重放请求读取现有 Task；前置拒绝则返回 4xx/5xx。

本项目不会转发任意上游 path 或 query。管理日志/充值历史 operation 未描述分页参数，Client 不擅自添加参数；受限大小的返回先在 Gateway 内分页。如果实际部署只返回上游默认一页，Gateway 无法猜测缺失的翻页协议，部署方需要补全文档。
