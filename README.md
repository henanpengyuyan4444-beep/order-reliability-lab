# Go 订单可靠性学习实验室

面向交易系统岗位的**开源学习与改造项目**。基础是 [maragudk/goqite v0.4.0](https://github.com/maragudk/goqite/tree/v0.4.0)（MIT）。学习补充由助手协助编写，不代表仓库所有者已独立完成或掌握。来源和改动见 [PROVENANCE.md](PROVENANCE.md)。

只处理虚构订单。两个本机 SQLite 文件分别表示应用与模拟下游；所有 HTTP 只绑定 127.0.0.1。没有真实资金、券商、支付账户或外部 Agent API。

## 从这里开始

1. [按 JD 倒推的阶段计划和验收](docs/learning-plan.md)
2. [逐模块阅读、修改路线](docs/reading-guide.md)
3. [故障演示手册](docs/fault-drills.md)
4. [面试问题与表述边界](docs/interview.md)
5. [Agent harness 与敏感数据练习](docs/agent-harness.md)
6. [保证、假设与未实现内容](docs/design.md)
7. [实际验证记录](docs/validation.md)
8. [本人验收记录：初始全部待验收](docs/learner-scorecard.md)
9. [定向简历与并行申请准备](docs/application-notes.md)

## 在这台电脑运行

工作目录：`G:\CodexLabs\order-reliability-lab`。已准备仅用于本项目的 Go 工具，不修改系统 PATH；SQLite 驱动需要系统现有 GCC。

```powershell
cd G:\CodexLabs\order-reliability-lab
.\scripts\verify.ps1
.\scripts\verify.ps1 -Race
.\scripts\run.ps1
```

另开一个 PowerShell：

```powershell
cd G:\CodexLabs\order-reliability-lab
.\scripts\demo.ps1
```

运行脚本只在当前 PowerShell 进程设置 PATH、Go 缓存和临时目录，所有这些目录均在本项目的 `.tools` 内。脚本无下载、无真实 API 调用、无删除操作。若 PowerShell 执行策略拦截，请检查脚本并按自己的系统策略处理，不自动降低策略。

换电脑克隆后安装 Go 1.24+ 和受支持的 C 编译器，执行 `go mod download`，再运行上述脚本；Linux/macOS 可直接 `go test ./...`、`go test -race ./...`、`go run ./cmd/lab`。本次是否验证其他平台见验证记录。

## 这一小项目证明什么

| 场景 | 实现 |
|---|---|
| 同键同参并发 | 数据库唯一键与短事务，返回相同订单 |
| 同键异参 | HTTP 409，拒绝复用 |
| 创建后退出 | 订单与 goqite 消息在同一 SQLite 事务提交 |
| 下游受理、响应丢失 | 状态保留 unknown，按原业务号查回结果 |
| 下游明确查不到 | 按同一个业务号重试，不换号 |
| 服务重启 | 持久化订单和队列；租约到期后重新领取 |
| 旧 Worker 迟到、重复消息 | attempt version 条件更新，终态不回退 |
| 一直无法确认 | 有限指数退避，进入 needs_review，不伪装失败 |
| 并发与排查 | 固定 Worker 数；JSON 日志含 request_id、order_id、attempt |

下游的幂等与可查询能力是**模拟服务的约定**。本项目没有证明任意外部系统的端到端 exactly-once，没有正式对账、手工审核后台、容灾、资金账本、鉴权或生产部署能力。

## HTTP

`POST /orders`，头 `Idempotency-Key: demo-1`，可选 `X-Request-ID: req-1`：

```json
{"amount_minor":1200,"scenario":"commit_timeout"}
```

首次接收 202；同键同参返回 200；冲突 409；非法输入 400。202 仅表示本地已接收。

`GET /orders/{id}` 查询状态；`GET /lab/effects/{id}` 查看模拟下游实际作用次数及 POST 次数；`GET /health` 为存活检查，不代表下游就绪。

scenario 可为 normal、commit_timeout、transient、unavailable、reject、malformed、query_unavailable。它是测试注入字段，不应在生产 API 暴露。

## 项目阶段

软件验证记录与“用户学会”分开。先交付可用项目与真实定向简历，学习、面试准备和投递准备并行；benchmark 不是投递硬门槛。尚未完成用户独立验收；本轮未投递公司，也不承诺通过面试。已有真实 AI 应用项目继续作为主要经历举证，不需要另凑聊天机器人。
