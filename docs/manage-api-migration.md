# Manage API adapter migration

## Sources and current Gateway flow

Compare `kovar-manage-api.json` (131 paths), `new-kovar-manage-api.json` (218 paths), and `kovar-new-api.json` (Model API). Missing response details were checked against the adjacent `../kovar-new-api` checkout at `9e514e000ba32c2848d8eb6c4beec1f11c686a6e`, specifically `controller/user.go`, `controller/token.go`, `controller/topup.go`, `controller/topup_axone.go`, `controller/pricing.go`, and `common/constants.go`. This is source evidence, not verification of the deployed service.

New Manage document: 261 operations, SHA256 `929b0d98a0563b8a41f9fe840d9884e56020338991f41b0be9438950242443a0`. All three input API files were left unchanged.

Before migration:

- Auth: lowercase EVM address → EIP-191 signature → timestamp/nonce → approval/rate limit.
- Binding: login/2FA or management Access Token → authenticated self → encrypted credential, including verified user ID.
- Token: reserve unique local slot → create/search/get dedicated model Key → encrypted storage; no automatic write retry.
- Models: public listing and task admission both queried the dedicated Key's `/v1/models`.
- Pricing/account: binding credential → `/api/pricing`, `/api/user/self`; model usage uses dedicated Key.
- Topup: user info/history; history was paginated locally; creation returned NOT_SUPPORTED inside existing persistent idempotency wrapper.
- Tasks: `payload` → dedicated Key/model availability → price/budget checks → persistent task/idempotency → Model API → result/usage.
- Fixed HTTP routes define the public allowlist. `configs/router.json` configures task model selection and pricing, not a general upstream proxy.

## API migration matrix

“User auth” below means SessionAuth + New-Api-User or management AccessToken + New-Api-User, derived from the binding. Existing code already supplied both.

| Gateway capability | Current upstream endpoint | New upstream endpoint | Changed? | Auth change | Request change | Response change | Required code change |
|---|---|---|---|---|---|---|---|
| Register/login/2FA | `/api/user/register`, `/api/user/login`, `/api/user/login/2fa` | Same | Documentation | None; pending session retained | Turnstile conditional on deployment; deferred | Generic ApiResponse remains | Keep workflow and sanitize failures |
| Binding/account | `/api/user/self` | Same | Documentation | Explicit User auth; already implemented | None | Same documented user fields | Keep identity confirmation |
| Public models | `/v1/models` | `/api/user/models` | Yes | Dedicated Key → User auth | None | Source confirms string array, possibly null when empty | Convert to existing `{data:[{id}]}`; never use dashboard/admin lists |
| Dashboard/admin models | Not used | `/api/models`, `/api/channel/models_enabled` | No Gateway change | User/Admin respectively | None | Not relevant | Do not expose |
| Pricing | `/api/pricing` | Same | Permission behavior clarified | HeaderNavModules/requireAuth; always use binding | None | HTTP 403 for denied module; success/message | Stable status mapping and credential redaction; retain raw data/pricing configuration |
| Topup info | `/api/user/topup/info` | Same | Documentation | Explicit User auth | None | Flexible data | Retain sanitized data and explicit provider selection |
| Topup history | `/api/user/topup/self` | Same | Pagination specified | User auth | Gateway page → p; page_size; optional keyword | PageInfo.items/total | Forward bounded paging; no second local slice |
| Topup status | None | `/api/user/topup/status` | Addition | User auth; controller checks order owner | Required trade_no | Source: trade_no/status/complete_time/amount/money | Minimal signed GET route, match trade number, preserve raw amounts/status |
| Axone chains | None | `/api/user/axone/chains` | Addition | User auth | None | Flexible data | Minimal signed GET route |
| Axone order | NOT_SUPPORTED | `/api/user/axone/order` | Addition | User auth | amount int64, currency, chain_id, payment_wallet_address | Source returns pending order/payment details | Existing provider/payload + idempotency; validate, never treat creation as payment |
| Other topup providers | NOT_SUPPORTED | Existing epay/stripe/creem paths | New request schemas | User auth | Now documented | Provider-specific success/message formats | Deferred; preserve NOT_SUPPORTED, do not send charges |
| Dedicated Key creation | `/api/token/`, `/api/token/search`, `/api/token/{id}` | Same + POST `/api/token/{id}/key` | Breaking | User auth | Bounded search; explicit key retrieval after ownership check | Search/get keys now masked | Never store masked Key; keep reconciliation on ambiguity |
| Management Access Token | Not generated | `/api/user/token` | Clarification | User auth | None | Management token, not model Key | Do not call or mix with dedicated Key |
| Token usage | `/api/usage/token/` | Same | Clarification | Model TokenAuth only; no New-Api-User | None | Generic data | Preserve |
| Tasks and model operations | `/v1/models`, supported `/v1/*` operations | Same | No | Dedicated model Key | None | None | Preserve task contract, exact model selection, SSE and idempotency |
| Logs/data/personal tasks | Existing `/api/*/self` reads | Same | Documentation | User auth | Existing Gateway behavior | Generic data | No unrelated expansion |

## Breaking changes and minimal implementation

The masked token key is a functional breaking change. Pagination, explicit management authentication and pricing permissions require adapter validation even where paths are unchanged. No existing Gateway route or request field is renamed.

Non-breaking additions used here: topup status, Axone chains and order. Keep Axone address (alias of order), wallets, PayGo sessions, callbacks, other providers, and conditional Turnstile login support for separate work. Do not add database tables, environment variables, helper operations, automatic wallet transfers or new infrastructure.

Implement client/mapper/service first, then the two new fixed read routes and existing history query mapping. Verify HTTP/business errors, malformed data, both credential types, user isolation, masked key retrieval, paging and payment/task idempotency using mock upstream plus isolated PostgreSQL tests.

## Response and deployment constraints

- Controller `GetUserModels` returns `[]string`; no model metadata is inferred. Public discovery uses User auth. Task execution continues to require the dedicated Key and its `/v1/models` check.
- Topup status returns Kovar's raw status. Source constants are `pending`, `success`, `failed`, `expired`; do not relabel creation as paid/completed. Preserve other nonempty provider statuses without interpreting them as quota credit. Quota is refreshed only through the existing account API.
- `GetTopUpStatus` checks the authenticated user against the order owner. The Gateway supplies only bound credentials and `trade_no`, never a caller-supplied user identity. No admin endpoint fallback is permitted.
- Axone success returns a pending order. `success=false` or the controller's legacy `message:error` is an upstream rejection; raw upstream messages are never returned to the Agent.
- Remaining flexible data is sanitized JSON, with exact numeric bytes retained rather than converting money through float64. Production controller/schema drift, provider enablement/minimum amount/chain support, pricing configuration and actual payment settlement require deployment verification.

## Delivered changes and compatibility

| File | Reason |
|---|---|
| `internal/client/kovarmanage/client.go` | Manage HTTP/business error mapping; message:error handling; masked Key discovery and full Key retrieval; Axone provider dispatch |
| `internal/client/kovarmanage/account.go` (new) | User models, bounded topup paging/filter, ownership-scoped status query, Axone chains/order validation; preserve raw amounts |
| `internal/platform/upstream/client.go` | Retain upstream HTTP status for Manage mapping while preserving Model error behavior through Unwrap |
| `internal/binding/service.go` | Resolve binding credentials, invoke adapters, redact returned data; remove second topup pagination |
| `internal/binding/redact.go` | Redact credentials even when flexible data is a scalar string |
| `internal/task/service.go` | Convert user model IDs to the existing public model response; task execution remains unchanged |
| `internal/app/server.go` | Two fixed signed read routes; forward only topup history keyword in addition to existing paging |
| `internal/client/kovarmanage/client_test.go` | Update existing token fixture for the full Key endpoint |
| `internal/client/kovarmanage/migration_test.go` (new) | Both credential types, model/pricing/topup reads, HTTP/business errors, invalid responses, amount precision, ownership/ambiguity checks and no POST retry |
| `internal/binding/redact_test.go` | Scalar credential and exact decimal redaction regressions |
| `tests/gateway_test.go` | Update mock to source-confirmed masked Key and paginated Manage behavior; preserve existing workflow tests |
| `tests/manage_migration_test.go` (new) | Signed model discovery before Key creation, history, spoofed identity headers, two-user order isolation, Axone replay/conflict/failure and unchanged quota |
| `README.md` | Update supported capabilities and known limitations; preserve the user's existing remote database edit |
| `docs/gateway-api.md` | Document added routes, Axone payload, pagination and error mapping |
| `docs/source-contracts.md` | Link the new authoritative document analysis while preserving historical inventories |
| `docs/manage-api-migration.md` (new) | Current flow, full matrix, source evidence, compatibility, verification and deferred work |

- Public API: added only `GET /api/v1/account/topup/status` and `GET /api/v1/account/topup/axone/chains`; existing topup body gains supported provider `axone`, and history accepts optional `keyword`. Existing routes/fields are retained. Models now describe the bound user's availability; task admission still verifies the dedicated Key. Manage error status changes are intentional and documented in gateway-api.md.
- DB schema/migrations: unchanged. Agent ID, registration, signature format, nonce/replay protection, budget checks, idempotency and helper contract: unchanged.
- New environment variables/dependencies/build tooling: none.

## Verification — 2026-09-18

| Check | Result |
|---|---|
| gofmt on changed Go files | Passed; final `gofmt -l` empty |
| `git diff --check` | Passed |
| `go test ./...` | Passed; without TEST_DATABASE_URL the DB cases skip |
| `go test -race ./...` with TEST_DATABASE_URL | Passed, including all PostgreSQL integration cases and migration tests; Gateway tests 32.618s |
| `make lint` (`go vet ./...`) | Passed |
| `make build` | Passed; gateway and migrate binaries built |

The existing local PostgreSQL container rejected the initial test connection. The completed integration run used a separate temporary local container from the already installed PostgreSQL image, with random credentials kept out of output and independent test schemas. The temporary container was removed afterward; no existing or remote database was changed. Every Kovar call in tests used httptest mocks. No live login, model charge, wallet transfer or payment was performed.

Unconfirmed deployment behavior: whether the deployed controllers match this checkout, real pricing shape/units and account quota semantics, HeaderNavModules settings, Turnstile requirements, Axone enablement/chain/currency/minimums and settlement callbacks. Deferred capabilities are Axone address alias/wallets/PayGo, other payment providers, Turnstile integration, dashboard/admin model APIs and additional log pagination/filter adapters.
