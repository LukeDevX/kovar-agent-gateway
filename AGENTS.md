# AGENTS.md

## 1. 文件用途

本文件定义小型 Golang 后端项目中，AI Coding Agent 与人工开发者应遵守的工程规则。

适用目标：

- 保持代码正确、简单、清晰；
- 形成高内聚、低耦合的模块边界；
- 保持代码可维护、可测试、可扩展；
- 避免过度设计和无需求驱动的抽象；
- 对事务、并发、幂等、安全等高风险问题保持明确约束；
- 让 Agent 的每次修改都可解释、可验证、可回滚。

本规范主要适用于：

- 单体 Go API；
- 小型内部系统；
- CRUD 后台；
- 小型 SaaS；
- Web3 / RWA 辅助服务；
- 链上监听 API；
- 管理后台后端；
- 单实例或少量实例部署；
- PostgreSQL / MySQL + 可选 Redis 的普通业务系统。

典型规模：

```text
1~5 个开发者
单仓库
单服务或少量进程
中低并发
一个主要数据库
可选 Redis
少量第三方 API
```

本文件由人工维护。

Agent 在修改代码前 MUST 阅读并遵守本文件。

除非用户明确要求，否则 Agent MUST NOT 修改、重写、删除或重组本文件。

如果项目当前实现与本文件冲突，Agent SHOULD：

1. 优先理解现有实现和历史约束；
2. 明确指出冲突；
3. 优先做满足当前任务的最小修改；
4. 不因规范偏好擅自进行大规模重构。

规范关键词：

- **MUST**：必须遵守；
- **MUST NOT**：禁止；
- **SHOULD**：默认应遵守，除非有明确理由；
- **SHOULD NOT**：默认避免；
- **MAY**：可选。

---

# 2. 核心工程原则

工程决策默认按以下顺序权衡：

```text
正确性
>
简单性
>
清晰性
>
高内聚 / 低耦合
>
可测试性
>
可维护性
>
可扩展性
>
抽象程度
```

可扩展不等于提前建设复杂架构。

真正的可扩展性来自：

- 清晰的模块边界；
- 明确的数据所有权；
- 稳定且尽量小的依赖面；
- 可替换的外部系统边界；
- 可测试的业务逻辑；
- 数据库约束、事务、幂等和状态转换正确；
- 新需求可以局部修改，而不是牵动整个项目。

---

# 3. Agent 行为原则

本节吸收 `andrej-karpathy-skills` 的四个核心行为原则，并结合本项目工程约束落地：

```text
Think Before Coding
Simplicity First
Surgical Changes
Goal-Driven Execution
```

对应中文：先理解再编码、简单优先、精准修改、目标驱动执行。

本节用于约束 AI Coding Agent 的工作方式。

## 3.1 编码前先理解

Agent MUST NOT 在存在明显不确定性时默默做关键假设。

开始实现前 SHOULD 确认：

- 用户真正要求的结果是什么；
- 当前代码入口在哪里；
- 哪个业务模块拥有该能力；
- 当前项目已经采用什么模式；
- 是否存在多种合理实现；
- 是否涉及兼容性、数据迁移、事务、并发、幂等或安全风险。

如果存在多个合理方案，Agent SHOULD 简短说明关键权衡。

如果有更简单且同样满足需求的方案，Agent SHOULD 优先选择更简单方案。

对于明显错误、危险或成本过高的设计要求，Agent SHOULD 指出风险，而不是机械执行。

## 3.2 简单优先

只实现当前需求需要的能力。

MUST NOT 因为“未来可能需要”主动增加：

- 微服务；
- MQ；
- Kafka；
- RabbitMQ；
- 分布式锁；
- Leader Election；
- Event Sourcing；
- CQRS；
- Service Mesh；
- 复杂 DDD；
- 多数据库兼容层；
- 通用插件系统；
- 自研任务调度框架；
- 无需求驱动的配置系统；
- 无实际使用场景的扩展点。

如果普通函数或具体类型可以解决问题，就不要自动创建：

```text
interface
+ factory
+ manager
+ registry
+ provider
+ adapter
```

只有真实边界、真实替换需求或稳定复用场景出现时再抽象。

## 3.3 精准修改

Agent MUST 尽量只修改完成当前任务所必需的代码。

MUST NOT：

- 顺便格式化整个项目；
- 顺便重命名无关代码；
- 顺便移动无关文件；
- 顺便重构相邻模块；
- 删除与本次任务无关的旧代码；
- 因个人偏好替换项目现有风格。

如果发现与任务无关的问题：

- 可以指出；
- 不要擅自修改。

如果本次改动导致某些 import、变量、函数或代码分支变为无用，Agent SHOULD 清理这些由本次改动产生的孤儿代码。

检验标准：

> 每一处修改都应该能够直接追溯到当前需求、正确性修复或必要测试。

## 3.4 目标驱动执行

非简单任务开始前 SHOULD 明确成功标准。

例如：

```text
“增加输入校验”
→ 无效输入测试失败
→ 实现校验
→ 测试通过
```

```text
“修复 bug”
→ 先复现 bug
→ 增加 regression test
→ 修复
→ 测试通过
```

```text
“重构模块”
→ 修改前测试通过
→ 重构
→ 修改后测试仍通过
```

对于多步骤任务，Agent SHOULD 使用简短计划：

```text
1. 修改 X → 验证 Y
2. 修改 A → 验证 B
3. 执行测试 → 确认结果
```

Agent MUST NOT 以“代码已经写完”作为完成标准。

完成标准必须包含验证。

---

# 4. 高内聚、低耦合

## 4.1 高内聚

同一个业务能力相关的代码 SHOULD 尽量放在同一个业务模块中。

例如：

```text
internal/
  user/
    handler.go
    service.go
    repository.go
    model.go
    errors.go
    service_test.go

  payment/
    handler.go
    service.go
    repository.go
    client.go
    model.go
    errors.go
    service_test.go
```

目标是：

```text
修改 user
→ 主要修改 internal/user

修改 payment
→ 主要修改 internal/payment
```

而不是：

```text
修改 payment
→ handler/
→ service/
→ repository/
→ model/
→ client/
→ utils/
```

散落多个全局技术目录。

## 4.2 低耦合

一个业务模块 SHOULD 只暴露其他模块真正需要的能力。

模块 MUST NOT 通过以下方式互相耦合：

- 直接访问其他模块 Repository；
- 直接操作其他模块的数据表；
- 依赖其他模块私有数据库模型；
- 修改其他模块内部状态；
- 共享大量可变全局状态；
- 通过 `common` / `utils` / `shared` 隐式共享业务逻辑。

跨模块调用优先依赖稳定的 Service / Facade 能力。

例如：

```text
order
  ↓
payment.Service
```

而不是：

```text
order
  ↓
payment.Repository
  ↓
payment 表
```

## 4.3 数据所有权

每个业务模块 SHOULD 明确自己主要负责的数据。

例如：

```text
user    → users
order   → orders / order_items
payment → payments / payment_events
```

一个模块如果需要修改另一个模块拥有的数据，优先通过该模块的业务能力完成。

MUST NOT 为了方便在多个模块中复制同一份业务写逻辑。

## 4.4 避免循环依赖

模块依赖关系 SHOULD 保持单向。

出现以下情况时说明边界可能有问题：

```text
A 依赖 B
B 又依赖 A
```

此时优先考虑：

1. 是否职责拆错；
2. 是否存在可以下沉的稳定小能力；
3. 是否应该通过调用方定义的小接口解耦；
4. 是否实际上属于同一个业务模块。

MUST NOT 为了解决循环依赖直接创建新的巨大 `shared` 包。

---

# 5. 推荐目录

默认推荐“业务模块 + 平台能力”的结构。

```text
cmd/
  api/
    main.go

internal/
  user/
  order/
  payment/

  platform/
    config/
    database/
    cache/
    logging/
    httpserver/

  middleware/

migrations/
scripts/
deploy/
```

业务模块内部按实际复杂度组织。

小模块可以直接：

```text
internal/user/
  handler.go
  service.go
  repository.go
  model.go
```

复杂一些时可以继续拆分：

```text
internal/payment/
  handler/
  service/
  repository/
  client/
  model/
```

不要为了目录形式强制拆包。

## 5.1 platform 的边界

`internal/platform` 只放真正跨业务、与具体业务无关的基础能力，例如：

- config；
- database connection；
- Redis connection；
- logger；
- HTTP server bootstrap；
- metrics；
- tracing。

MUST NOT 把具体业务规则放入 `platform`。

## 5.2 pkg 的边界

`pkg/` 只用于：

> 可以脱离当前项目业务独立复用的代码。

如果代码只服务当前项目，优先放 `internal/`。

## 5.3 禁止杂物包

MUST NOT 创建不断膨胀的：

```text
common
utils
helpers
shared
misc
```

如果 helper 只属于 payment，就放回 payment。

如果 helper 只属于 user，就放回 user。

---

# 6. 模块内部职责

模块内部默认调用方向：

```text
HTTP / Job / Consumer
        ↓
     Handler
        ↓
     Service
      ↙   ↘
Repository Client
     ↓       ↓
    DB   External API
```

## 6.1 Handler

Handler 只负责：

1. 解析输入；
2. 校验基础格式；
3. 获取认证上下文；
4. 调用 Service；
5. 映射 HTTP response。

Handler MUST NOT：

- 编写 SQL；
- 开启业务事务；
- 实现资金计算；
- 实现复杂状态转换；
- 承担多步骤业务编排；
- 为完成一个业务流程直接调用多个 Repository。

## 6.2 Service

Service 负责：

- 业务规则；
- 业务编排；
- 权限相关业务判断；
- 事务语义；
- 状态转换；
- 幂等流程；
- 多 Repository / Client 协作。

Service SHOULD 尽量与 HTTP 框架解耦。

MUST NOT 在 Service 中直接依赖 `gin.Context`、`echo.Context` 等 Web Framework Context。

使用：

```go
context.Context
```

传播请求生命周期。

## 6.3 Repository

Repository 负责：

- 数据查询；
- 数据写入；
- 条件更新；
- 行锁；
- 持久化相关映射。

Repository SHOULD 保持业务语义清晰。

优先：

```go
GetByID
Create
UpdateStatus
ReserveBalance
MarkProcessed
```

而不是构建复杂的：

```text
BaseRepository
GenericRepository
UniversalRepository
RepositoryManager
```

不要为了减少少量 CRUD 重复引入复杂泛型仓储。

## 6.4 Client

第三方 HTTP / RPC / SDK 调用 SHOULD 封装到业务模块自己的 client 或明确的 integration 边界中。

例如：

```text
internal/payment/provider_client.go
```

或：

```text
internal/integration/fireblocks/
```

Client SHOULD 隐藏第三方 SDK 的具体错误和数据结构，避免它们扩散到整个项目。

---

# 7. 依赖注入与接口

依赖 SHOULD 通过构造函数显式注入。

例如：

```go
type Service struct {
    repo *Repository
}

func NewService(repo *Repository) *Service {
    return &Service{
        repo: repo,
    }
}
```

MUST NOT 为新的业务代码引入全局可变 service locator：

```go
var DB *gorm.DB
var RedisClient *redis.Client
var UserServiceInstance *UserService
```

## 7.1 不要为了测试到处创建 interface

Go interface SHOULD 在有明确边界时使用，例如：

- 第三方服务；
- 多实现策略；
- 需要替换的时钟；
- 文件存储；
- 消息发送；
- 调用方确实需要隔离依赖。

如果只有一个实现且没有实际替换需求，具体类型通常更简单。

接口 SHOULD 尽量小，并优先由使用方定义。

例如：

```go
type UserReader interface {
    GetByID(ctx context.Context, id int64) (*User, error)
}
```

而不是提前创建包含几十个方法的通用 Repository interface。

---

# 8. Context

所有 I/O 调用 SHOULD 传播 `context.Context`。

推荐调用链：

```text
Handler
→ Service
→ Repository / Redis / HTTP Client
```

数据库：

```go
db.WithContext(ctx)
```

HTTP：

```go
http.NewRequestWithContext(ctx, ...)
```

Redis：

```go
client.Get(ctx, key)
```

业务 I/O helper MUST NOT 使用：

```go
context.Background()
```

覆盖调用方已经传入的 context。

只有明确脱离请求生命周期的后台任务才 MAY 创建新的根 context。

---

# 9. 输入校验

所有外部输入 MUST 校验。

包括：

- path；
- query；
- JSON body；
- enum；
- pagination；
- URL；
- 文件上传；
- Webhook body；
- 第三方回调数据。

分页 MUST 有默认值和最大值。

例如：

```go
if req.PageSize <= 0 {
    req.PageSize = 20
}

if req.PageSize > 100 {
    req.PageSize = 100
}
```

MUST NOT 提供无限制 list API。

---

# 10. 错误处理

错误包装使用 `%w`：

```go
return fmt.Errorf("get user %d: %w", id, err)
```

领域错误可以定义为：

```go
var (
    ErrNotFound     = errors.New("not found")
    ErrConflict     = errors.New("conflict")
    ErrInvalidState = errors.New("invalid state")
)
```

判断错误使用：

```go
errors.Is(err, ErrNotFound)
```

MUST NOT：

```go
if err.Error() == "xxx"
```

MUST NOT 将原始：

- SQL error；
- Redis error；
- panic；
- SDK error；
- stack trace；

直接返回客户端。

错误信息 SHOULD：

- 对外稳定；
- 对内可定位；
- 保留原始 error chain。

---

# 11. 配置与 Secret

配置 SHOULD 在启动阶段统一加载。

例如：

```go
type Config struct {
    Server   ServerConfig
    Database DatabaseConfig
    Redis    RedisConfig
}
```

MUST：

- 生产环境关键配置缺失时启动失败；
- Secret 来自环境变量或 Secret Manager；
- Secret 不提交到仓库；
- Secret 不写入日志。

业务代码 SHOULD NOT 到处直接：

```go
os.Getenv(...)
```

配置读取应集中管理。

禁止提交或记录：

```text
password
private key
mnemonic
API secret
DB password
Redis password
JWT secret
webhook secret
access token
refresh token
Authorization header
Cookie
完整 DSN
```

示例配置使用：

```text
YOUR_API_KEY
CHANGE_ME
example
```

---

# 12. 数据库设计

MUST：

- Repository 的 I/O 接收 context；
- 数据库连接池显式配置；
- 核心唯一性尽量由数据库 constraint 保证；
- 热点查询根据真实查询模式建立索引；
- list API 分页；
- schema 变更使用 migration；
- 生产环境 migration 可追踪。

生产环境 SHOULD NOT 依赖应用启动时自动执行：

```go
AutoMigrate()
```

开发环境 MAY 根据项目需要使用。

---

# 13. 金额与精度

金额、Token balance、资产数量等权威数据 MUST NOT 使用：

```go
float32
float64
```

优先使用：

```text
整数最小单位
```

例如：

```go
amount int64
```

或者可靠的 Decimal / big.Int 类型。

Web3 场景 SHOULD 明确：

- token decimals；
- rounding 规则；
- 链上整数单位；
- 链下展示单位；
- overflow / underflow 风险。

---

# 14. 事务

涉及多个必须共同成功或失败的相关写操作时，Service SHOULD 定义事务边界。

例如：

```text
创建订单
+
扣减余额
+
写入账本
```

应该在同一事务中完成。

MUST：

- 出错 rollback；
- 不吞事务错误；
- DB commit 后再更新 cache；
- 不在事务中执行不必要的慢速外部 HTTP 请求。

Service 负责事务语义，但不要为了“纯架构”引入复杂 Transaction Manager。

如果当前 ORM 支持简单事务回调，优先沿用项目已有方式。

---

# 15. 并发

不要假设请求一定串行执行。

多个请求可能同时修改同一数据时 MUST 考虑竞争条件。

优先使用数据库保证：

- unique constraint；
- conditional update；
- atomic SQL；
- row lock；
- version / CAS。

例如：

```sql
UPDATE jobs
SET status = 'running'
WHERE id = ? AND status = 'pending';
```

必须检查：

```text
RowsAffected
```

MUST NOT 仅依赖：

```go
sync.Mutex
```

保证跨进程数据正确性。

对于单实例纯内存状态，mutex MAY 使用。

---

# 16. 幂等

任何可能被以下主体重试的写操作 SHOULD 考虑幂等：

- 客户端；
- Webhook provider；
- Job；
- 外部 API；
- 消费者；
- 链上事件同步器。

常见方案：

```text
business_key
+
unique constraint
+
transaction
```

支付：

```text
provider_order_id UNIQUE
```

Webhook：

```text
provider_event_id UNIQUE
```

创建类 API 可以按需求支持：

```text
Idempotency-Key
```

MUST NOT 假设：

```text
前端不会重复提交
```

MUST NOT 在没有幂等保障时自动重试非幂等写请求。

---

# 17. 状态转换

如果业务存在状态：

```text
pending
processing
success
failed
```

状态转换 SHOULD 显式校验。

优先：

```sql
UPDATE orders
SET status = 'processing'
WHERE id = ? AND status = 'pending';
```

而不是不加条件直接：

```go
db.Save(&order)
```

并发状态转换优先使用：

- conditional update；
- row lock；
- CAS / version。

复杂状态机出现前不要引入专门状态机框架。

---

# 18. Redis 与 Cache

Redis 是可选基础设施。

只有存在明确需求时使用：

- cache；
- rate limit；
- session；
- short-lived state；
- 明确需要的分布式协调。

DB 默认作为业务权威数据源。

推荐 cache-aside：

```text
read:
cache
  ↓ miss
DB
  ↓
cache set

write:
DB transaction
  ↓ commit
cache delete / update
```

每类缓存 SHOULD 明确：

- key；
- TTL；
- invalidation；
- Redis 失败时 fail open 还是 fail closed。

不要把 Redis 变成缺乏持久化语义的隐式主数据库。

---

# 19. 外部 API

共享 `http.Client`。

MUST 设置 timeout。

MUST NOT 每次请求创建：

```go
client := &http.Client{}
```

外部请求必须考虑：

- timeout；
- context；
- DNS / connection error；
- 非 2xx；
- response body close；
- body size；
- JSON decode error；
- provider rate limit。

第三方 SDK 的类型 SHOULD 尽量限制在对应 Client 边界内部。

---

# 20. 重试

不是所有错误都应该重试。

通常可重试：

- temporary network error；
- timeout；
- HTTP 429；
- HTTP 502；
- HTTP 503；
- HTTP 504。

通常不重试：

- HTTP 400；
- HTTP 401；
- HTTP 403；
- 参数错误；
- 权限错误；
- 明确业务拒绝。

重试 SHOULD：

```text
有限次数
+
指数退避
+
jitter
```

只有确认操作幂等，或拥有可靠幂等 key 时，才自动重试写请求。

对于第三方请求 timeout，必须考虑：

> 请求可能已经在对方成功执行，只是本地没有收到响应。

---

# 21. Goroutine 与后台任务

Goroutine 必须有明确生命周期。

MUST NOT 创建无法停止的后台循环：

```go
go func() {
    for {
        ...
    }
}()
```

长生命周期 goroutine SHOULD：

- 接收 context；
- 支持 shutdown；
- 明确 owner；
- 明确 panic 策略；
- 明确并发上限。

有界并发可以使用：

- errgroup；
- semaphore；
- worker pool。

不要无限启动 goroutine。

小系统后台任务优先：

```text
cron / ticker
→ Service
→ Repository
```

单实例 scheduler MUST 明确其单实例假设。

只有真实出现多实例协调需求时，再考虑：

- DB lock；
- DB lease；
- leader election；
- external scheduler。

不要提前实现。

---

# 22. MQ

默认：

```text
DO NOT USE MQ
```

如果：

```text
同步调用
+
数据库
+
简单 job
```

可以稳定解决问题，就不要引入 MQ。

只有存在明确需求时才考虑：

- Kafka；
- RabbitMQ；
- SQS；
- NATS。

例如：

- 需要可靠异步处理；
- 明确削峰；
- 服务间真正解耦；
- 任务需要持久化恢复；
- 事件吞吐量已经证明同步方式不足。

---

# 23. 认证与授权

Authentication 与 Authorization MUST 分开处理。

```text
Authentication:
这个用户是谁？

Authorization:
这个用户能不能执行这个操作？
```

Admin API MUST 在后端检查权限。

MUST NOT 只依赖前端隐藏按钮。

MUST NOT 信任客户端直接传入的：

```text
user_id
role
is_admin
```

除非已经经过可信认证机制验证。

---

# 24. Webhook

Webhook SHOULD：

1. 限制 body size；
2. 校验 signature；
3. 校验 event / provider / account；
4. 根据 event ID 保证幂等；
5. 在合理事务边界中完成业务更新；
6. 返回 provider 需要的正确 HTTP 状态。

不能只信任 callback 中的：

```text
amount
user_id
order_id
```

必须与本地业务数据校验。

---

# 25. 日志

使用结构化日志。

推荐基础字段：

```text
request_id
user_id
module
operation
duration_ms
error
```

业务相关时增加：

```text
order_id
task_id
tx_hash
provider
```

日志 SHOULD 能回答：

- 哪个请求失败；
- 哪个模块失败；
- 哪一步失败；
- 对应业务 ID 是什么；
- 花费多久。

MUST NOT 记录 Secret。

日志内容 SHOULD 避免存储无必要的敏感业务数据。

---

# 26. 测试

测试重点不是追求 100% coverage，而是保护高风险行为。

优先测试：

1. Service 业务规则；
2. Repository 关键查询 / 写入；
3. Handler 输入边界；
4. 事务；
5. 幂等；
6. 并发状态更新；
7. 权限；
8. 金额；
9. Webhook；
10. 重试。

修复 bug SHOULD 增加 regression test。

重构 SHOULD 确保修改前后行为测试一致。

如果某个行为无法合理自动测试，Agent SHOULD 明确说明使用了什么替代验证方式。

---

# 27. Go 基础质量

提交前 SHOULD 执行：

```bash
gofmt
go vet ./...
go test ./...
```

涉及并发代码时 SHOULD 执行：

```bash
go test -race ./...
```

按项目配置可执行：

```bash
staticcheck ./...
golangci-lint run
govulncheck ./...
```

Agent MUST NOT 声称测试通过，除非实际执行成功。

如果因环境原因无法运行测试，必须明确说明未验证项。

---

# 28. 函数设计与命名

函数 SHOULD：

- single responsibility；
- early return；
- 参数数量合理；
- side effect 清晰；
- 错误表达失败原因；
- 不隐藏重要 I/O；
- 不依赖隐式全局状态。

参数过多时可以使用 request / options struct。

避免：

```go
func Process(a, b, c, d, e, f, g, h string)
```

Package 名 SHOULD 简短并表达职责：

```text
user
order
payment
config
database
```

避免：

```text
common
misc
helper
util2
manager2
```

Go interface 不需要统一 `I` 前缀。

---

# 29. 注释

注释优先解释：

```text
为什么
```

而不是重复：

```text
代码正在做什么
```

好：

```go
// Lock the account row because balance validation and deduction
// must be performed atomically under concurrent requests.
```

差：

```go
// Get user
user := getUser()
```

特殊约束、并发原因、安全原因、协议行为 SHOULD 留下注释。

---

# 30. 性能

默认先写正确代码。

MUST 避免明显问题：

- 无分页 list；
- N+1 query；
- 无限 goroutine；
- 无限 request body；
- 每请求创建 HTTP client；
- 一次加载超大数据集；
- 超大批量一次性写入。

只有出现真实性能问题后，再基于：

- benchmark；
- pprof；
- tracing；
- query plan；
- metrics；

进行优化。

不要凭感觉提前增加 cache、并发或复杂批处理。

---

# 31. Docker 与运行时

如果使用 Docker，优先 multi-stage build。

生产 image SHOULD：

- 尽量小；
- 非 root；
- 不包含源码；
- 不包含 Secret；
- 版本可追踪。

Web 服务 SHOULD 支持 graceful shutdown。

收到退出信号后合理顺序：

```text
停止接收新请求
→ cancel context
→ 停止 job / worker
→ HTTP shutdown
→ 关闭 Redis
→ 关闭 DB
```

健康检查根据部署需求提供：

```text
/health
```

需要区分时再拆：

```text
/liveness
/readiness
```

不要为了形式强制增加无意义 endpoint。

---

# 32. 新功能的扩展策略

新增功能时按以下顺序决策：

## 第一步：能否在现有模块内简单扩展？

如果可以：

```text
沿用现有结构
```

不要创建新架构。

## 第二步：是否已经形成独立业务能力？

当一个能力具备明显独立的：

- 业务规则；
- 数据所有权；
- 生命周期；
- 外部依赖；
- 测试边界；

可以拆成独立业务模块。

## 第三步：是否真的需要抽象？

稳定模式在多个独立场景中反复出现后，再考虑抽象。

推荐经验：

```text
同一种稳定模式
至少出现 2~3 个真实使用场景
```

第一次出现时不要急于构建通用框架。

## 第四步：是否真的需要新基础设施？

只有当现有方案已经出现明确问题时，再升级：

```text
Redis
MQ
Distributed Lock
Leader Election
Worker System
Microservice
```

新基础设施 MUST 能对应一个已经存在的真实问题，而不是假想未来需求。

---

# 33. Agent 修改代码前检查

Agent 在非简单修改前 SHOULD 检查：

- [ ] 用户要求和成功标准是什么？
- [ ] 是否存在需要明确的关键假设？
- [ ] 代码入口在哪里？
- [ ] 哪个业务模块拥有这个能力？
- [ ] 当前项目已有类似实现吗？
- [ ] 能否复用现有模式而不是创建新抽象？
- [ ] 是否会增加跨模块耦合？
- [ ] 是否涉及事务？
- [ ] 是否涉及并发？
- [ ] 是否涉及幂等？
- [ ] 是否涉及状态转换？
- [ ] 是否涉及金额或精度？
- [ ] 是否涉及权限或 Secret？
- [ ] 是否已有测试？
- [ ] 如何验证修改成功？

---

# 34. Agent 修改原则

Agent SHOULD：

- 优先修改最少代码；
- 优先在拥有该业务能力的模块内修改；
- 优先沿用当前项目风格；
- 新增 abstraction 前先搜索已有实现；
- 修改行为时同步补测试；
- 只清理本次修改造成的无用代码；
- 保持 diff 易于 Review。

MUST NOT：

- 因为一个 bug 重构整个项目；
- 因为新增一个 API 迁移架构；
- 因为个人偏好替换 ORM / Router / Logger；
- 为一次性代码创建通用框架；
- 为“以后可能用”增加大量配置；
- 通过新增 `shared` / `common` 掩盖模块边界问题。

---

# 35. Review Checklist

Reviewer / Agent 在重要变更中 SHOULD 检查：

```text
这个修改是否属于正确的业务模块？

业务相关代码是否被不必要地散落到多个全局目录？

模块是否访问了其他模块的 Repository / 表 / 私有模型？

新增依赖是否真的必要？

这个请求是否可能重复执行？

两个请求是否可能同时执行？

数据库是否真正保证唯一性？

事务失败后数据是否一致？

DB commit 后 cache 更新失败会怎样？

第三方请求 timeout 后是否可能已经成功？

是否错误地重试了非幂等请求？

context cancellation 是否能传递到 I/O？

日志是否可能泄露 Secret？

list / body / goroutine 是否存在无界增长？

是否新增了单次使用的 abstraction？

是否修改了与当前任务无关的代码？

每一处 diff 是否都能解释？

是否存在更简单的实现？

测试是否真正验证了成功标准？
```

---

# 36. 完成定义

一个变更完成前 SHOULD 确认：

- [ ] 需求对应的成功标准已经满足；
- [ ] 修改范围与需求一致；
- [ ] 没有无关重构；
- [ ] 没有引入不必要 abstraction；
- [ ] 模块保持高内聚；
- [ ] 没有新增不合理跨模块耦合；
- [ ] 可以编译；
- [ ] gofmt 通过；
- [ ] go vet 通过；
- [ ] 测试通过；
- [ ] 需要时 race test 通过；
- [ ] 没有泄漏 Secret；
- [ ] I/O 使用正确 context；
- [ ] DB 写操作事务边界合理；
- [ ] 并发写不存在明显 race；
- [ ] 可重试写操作考虑幂等；
- [ ] API 输入有边界校验；
- [ ] 错误不会泄露内部实现；
- [ ] 没有提前引入复杂基础设施；
- [ ] Agent 已明确说明无法验证的内容。

---

# 37. Agent 最终输出要求

完成代码修改后，Agent SHOULD 简洁说明：

```text
修改了什么
为什么这样改
验证了什么
还有什么未验证 / 风险
```

不要只回复：

```text
Done
```

也不要输出大量与实际修改无关的架构说明。

---

# 38. 最终原则

对于小型 Go 后端：

```text
业务模块高内聚
+
模块之间低耦合
+
简单明确的调用方向
+
数据库约束
+
正确事务
+
必要幂等
+
有界并发
+
最小修改
+
明确验证
```

优先于：

```text
复杂分层
+
通用框架
+
提前抽象
+
假想扩展性
```

核心标准：

> 一个需求应该尽量只影响一个业务模块；一个模块应该尽量不需要知道另一个模块的内部实现。

> Minimum code. Clear boundaries. Surgical changes. Verifiable results.

**Simple first, correctness always.**
