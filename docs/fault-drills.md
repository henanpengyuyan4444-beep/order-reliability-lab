# 故障演示手册

仅使用本机虚构数据。建议先执行 scripts/verify.ps1，再用终端运行 scripts/run.ps1。另一终端执行 scripts/demo.ps1，它会创建本轮唯一业务键并验证 6 个场景。

| scenario | 注入点 | 预期观察 |
|---|---|---|
| normal | 无故障 | succeeded；effect=1，POST=1 |
| commit_timeout | 下游提交后延迟 1 秒，客户端 150ms 超时 | unknown 后查回 succeeded；effect=1，POST=1 |
| transient | 前两次 POST 返回 503 且未提交 | 先查不到，再同号重试；最终 effect=1，POST=3 |
| reject | 明确模拟业务拒绝 | rejected；effect=0，POST=1 |
| malformed | 提交后返回不可用的 200 内容 | unknown 后查询；effect=1，POST=1 |
| query_unavailable | 提交后响应丢失，之后查询一直 503 | needs_review；effect=1，POST=1，不能说失败 |

状态可能比轮询快，demo 的 observed 是实际看到的采样，不承诺包含每个中间状态。严格 unknown 断言在自动测试里。

## 独立复现重复/冲突

```powershell
$h=@{'Idempotency-Key'='my-demo-1';'X-Request-ID'='req-my-demo-1'}
$o=Invoke-RestMethod http://127.0.0.1:8080/orders -Method Post -Headers $h -ContentType 'application/json' -Body '{"amount_minor":1200,"scenario":"commit_timeout"}'
Invoke-RestMethod http://127.0.0.1:8080/orders -Method Post -Headers $h -ContentType 'application/json' -Body '{"amount_minor":1200,"scenario":"commit_timeout"}'
Invoke-RestMethod "http://127.0.0.1:8080/orders/$($o.id)"
Invoke-RestMethod "http://127.0.0.1:8080/lab/effects/$($o.id)"
# 下面故意冲突，预期 HTTP 409：
Invoke-RestMethod http://127.0.0.1:8080/orders -Method Post -Headers $h -ContentType 'application/json' -Body '{"amount_minor":1300,"scenario":"commit_timeout"}'
```

## 创建后重启

1. 用 `.\scripts\run.ps1 -Workers 0 -Data data-restart` 暂停 Worker。
2. 创建一个新键订单，确认 pending。
3. Ctrl+C 退出；保持数据目录不变。
4. 用 `.\scripts\run.ps1 -Workers 4 -Data data-restart` 重启，查询原 order.id，应收敛 succeeded。
5. 查看 effect=1。不要删数据库来“解决”恢复问题。

## 下游提交后应用突然退出

自动用例 `TestCrashAfterProviderCommit` 启动测试子进程；它持久化 claim、通过 HTTP 在独立 provider.db 提交，然后立即 os.Exit(23)，没有 finish 或 Close。父进程重开两个 DB，等待租约并查询原业务号；断言 POST=1、effect=1、attempts=2。

```powershell
. .\scripts\env.ps1
go test ./internal/lab -run '^TestCrashAfterProviderCommit$' -v -count=1
```

这是真实子进程突然退出边界，但不是机器断电或磁盘故障证明。

## 迟到结果与重复消息

`TestStaleAndDuplicateMessagesCannotRegressTerminal` 在测试中强制租约过期，让第二个 Worker 领取并完成，再送回第一个 Worker 的超时/拒绝结果，随后重新插入重复消息。必须保留 succeeded 且队列清空。仅测试代码直接改租约，HTTP 没有这种管理接口。

## 排查步骤

按 request_id 找 order_created / order_replayed，再用 order_id 找 order_claimed 和 order_result，确认 attempt、version、status、error_code。出现 stale_result_ignored 应查租约与请求耗时。记录原请求 ID 与本次重放请求 ID 的区别。

错误代码是固定分类，没有下游原始响应、金额或 Authorization 值。不要用真实身份证、账户、客户数据或令牌试验日志。人工复核需要保留证据，不应盲目更改状态或换业务号重新提交。
