# 逐模块阅读与动手路线

已有 Go 基础就从调用链开始，不必先刷语言入门。

| 顺序 | 阅读入口 | 必须回答 | 小修改及验收 |
|---|---|---|---|
| 1 | cmd/lab/main.go、http.go | 服务、Worker、两个 DB 怎样启动/退出？202 承诺了什么？ | 改端口启动，演示错误输入被拒；保留 loopback |
| 2 | store.go 的 schema/Create | 为什么金额用整数？同键异参为何拒绝？订单与队列如何原子提交？ | 增加可配置金额上限并补边界断言 |
| 3 | goqite.sql；上游 goqite.go 的 SendTx/ReceiveTx | 可见性超时是什么？领取不是完成，为什么？ | 在图中标出 commit 前/后的崩溃点；运行原子入队失败测试 |
| 4 | store.go 的 claim/finish | version 防什么？为什么先记 unknown？为什么预算在领取时增加？ | 修改重试预算，并补“最后一次尝试退出”的测试 |
| 5 | worker.go 的 Once/Client | 何时可以重发？为何 GET 失败不能推断没受理？ | 为“200 但业务号不匹配”加断言；解释已有响应验证 |
| 6 | mock.go | 为什么独立 DB？HTTP 取消为什么没有撤销已提交的 effect？ | 新增一种仅影响响应的故障，并证明 effect 没变 |
| 7 | Run 与日志 | Worker 数为什么有限？context 如何停止？日志如何串起来？ | 用 1 与 4 个 Worker 对比，不把小样本当性能报告 |
| 8 | docs/design.md | 哪些是模拟提供方合同，哪些是真实实现？ | 用自己的话重写一页设计和风险，不删掉边界 |

建议每次 60–90 分钟一小节，按验收推进，不以天数判断掌握。阅读顺序中涉及的测试可以用：

```powershell
. .\scripts\env.ps1
go test ./internal/lab -run TestKeyConflict -v -count=1
go test ./internal/lab -run 'TestCommittedTimeout|TestCrashAfter' -v -count=1
```

本人第一次改动建议选“金额上限配置 + 边界测试”，第二次选“重试错误分类”。先写失败测试，保存证据，再修改实现，跑相关用例及完整回归。每次提交应只含自己的改动；不要把助手初始提交重新署名为自己的从零实现。

## Elixir 小范围入门

先读官方 [GenServer](https://elixir.hexdocs.pm/GenServer.html) 与 [Supervisor](https://elixir.hexdocs.pm/Supervisor.html)。目标是能解释进程消息、状态、同步调用和监督重启。

本仓库提供 `exercises/elixir/review_worker.exs`：仅内存的人工复核任务列表，不接交易系统。安装官方 Elixir/OTP 后运行：

```text
elixir exercises/elixir/review_worker.exs
```

本机未检测到 Elixir/OTP，因此此文件**未执行验证**。任务：增加完成标记与重复 ID 处理测试；手工杀死 Worker，解释监督重启后内存状态为何消失。不要把这个练习说成持久化恢复方案或 Elixir 熟练经验。深度 OTP/Phoenix 作为后续学习，不妨碍先完成 Go 核心关卡。
