# 实际验证记录

验证日期：2026-11-05。工作目录：G:\CodexLabs\order-reliability-lab。以下是助手执行的结果，不是用户已掌握的证明。

## 结果

| 检查 | 实际结果 | 证据 |
|---|---|---|
| gofmt | 所有 Go 文件无格式差异 | scripts/verify.ps1 |
| go mod verify | all modules verified | evidence/module-verify.txt |
| go vet ./... | 退出码 0，无诊断 | evidence/vet.txt |
| go test -count=1 -v -cover ./... | 19 项顶层测试通过；1 个子进程辅助入口在普通运行时跳过，由崩溃父测试实际调用 | evidence/tests.txt |
| Go race 检查 | 退出码 0；同一组场景通过，未报告数据竞态 | evidence/race.txt |
| 内部包语句覆盖率 | 77.8%；cmd/lab 未设单元测试 | evidence/tests.txt |
| 编译程序 + 真实 HTTP 演示 | 6 种场景与一次停止/重启全部通过 | evidence/smoke-results.jsonl |
| 幂等负向对照 | 隔离副本移除冲突检查后 TestKeyConflict 正确失败；原文件未改变 | evidence/negative-control-result.json、negative-control.txt |
| 响应长度红绿测试 | 初版错误接受超长响应；加入精确长度校验后完整测试通过 | evidence/response-limit-red.txt、tests.txt |
| 来源核验 | 复制的 goqite SQL Git blob 与上游完全相同；SQLite 许可证字节一致 | evidence/source-manifest.json |

核心自动场景还包括：32 并发同键请求、异参 409、入队失败事务回滚、正常成功、受理后超时查单、明确未受理后重试、预算耗尽保留未知、业务拒绝、坏响应、待处理订单重开 DB、旧 Worker/重复消息、最后一次领取后中断、下游提交后子进程立即退出、Worker 数上限和取消、非法输入与日志哨兵、只允许 loopback、下游幂等。

## 本机实际演示值

| 场景 | 终态 | attempts | 下游 effect | POST |
|---|---:|---:|---:|---:|
| 关闭 Worker 后创建，再终止进程并重启 | succeeded | 1 | 1 | 1 |
| normal | succeeded | 1 | 1 | 1 |
| commit_timeout | succeeded | 2 | 1 | 1 |
| transient | succeeded | 3 | 1 | 3 |
| reject | rejected | 1 | 0 | 1 |
| query_unavailable | needs_review | 5 | 1 | 1 |
| malformed | succeeded | 2 | 1 | 1 |

attempts 是领取次数，POST 是模拟下游提交调用次数，两者不同。effect 只是模拟数据库中的作用记录，不是资金动作。

## 环境和已处理问题

- Windows amd64；Go 1.27.3；现有 MinGW GCC 8.1.0；CGO_ENABLED=1。
- Go 下载自 go.dev，核对发布 SHA256 后解压到项目 .tools；不会修改系统 Go。
- 构建缓存、模块缓存及正式验证的临时数据库位于 G: 项目的 .tools。确认盘符之前曾在默认 C: 目录准备初稿；在 G: 中继续修改、修复并执行了本报告的最终验证。
- 第一轮暴露 Windows SQLite URI 盘符处理错误，已修复为正确的 file URI，失败记录在 initial-windows-path-failure.txt。
- 普通 CGO 可执行文件在此旧 MinGW 环境被 Windows 报为无效 Win32 应用；去除调试符号后实际启动成功。verify/run 脚本固定使用 `-ldflags '-s -w'`。类似现象见 [Go 官方仓库 issue 77886](https://github.com/golang/go/issues/77886)。这是本机验证过的规避方式，不声称完成了编译器根因分析；产物不含完整调试符号。
- smoke 脚本只启停它创建的隐藏本机进程，演示后已停止服务。

## 未验证

Elixir 练习未运行（未检测到 Elixir/OTP）；Linux/macOS、多个服务进程/多机、长时间负载、硬件掉电、磁盘损坏、正式备份恢复、真实提供方与金融业务均未验证。没有上游全量兼容性/安全审计，也没有对 dque/asynq 跑测试。本报告不构成生产就绪或任意端到端 exactly-once 承诺。

## 复现

```powershell
.\scripts\verify.ps1
.\scripts\verify.ps1 -Race
.\scripts\negative-control.ps1
.\scripts\smoke.ps1
```

本机默认不需要 Docker/Redis。其他电脑需 Go 与兼容 C 编译器；Elixir 另行安装。阅读故障手册后，可不看脚本独立做一遍，再填写本人验收表。
