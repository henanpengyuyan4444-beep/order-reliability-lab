# 来源、许可证与修改说明

核对日期：2026-11-05。

## 选型比较

| 候选 | 核验的许可证 | 适合点 | 本次取舍 |
|---|---|---|---|
| [maragudk/goqite](https://github.com/maragudk/goqite) | [MIT，Maragu ApS](https://github.com/maragudk/goqite/blob/v0.4.0/LICENSE) | SQLite 一张队列表、可见性超时、事务接口；核心 Go 文件可完整读完 | 选择 v0.4.0 的 SQLite 路径，便于同事务写订单与任务 |
| [joncrlsn/dque](https://github.com/joncrlsn/dque) | [MIT，Jon Carlson](https://github.com/joncrlsn/dque/blob/v2/LICENSE) | 小型磁盘 FIFO，安全模式 fsync，便于研究持久化 | Dequeue 即移除；直接配合外部作用会留下崩溃窗口，补 ACK/事务需要更多工作 |
| [hibiken/asynq](https://github.com/hibiken/asynq) | [MIT，Kentaro Hibino](https://github.com/hibiken/asynq/blob/master/LICENSE) | Worker、重试、队列管理与监控成熟 | 需要 Redis；与订单数据库跨存储事务增大首个练习范围，留作后续对比 |

以上来自仓库 README、LICENSE 和接口源码；并未运行 dque 或 asynq，也未声称完成三者性能或安全审计。没有选大型商城。goqite 是队列库，本仓库是在其上补的小型业务实验室，不是从 GitHub 找到的完整交易系统。

## 锁定与保留

- goqite：`maragu.dev/goqite v0.4.0`，对应提交 [471f9d49ce356737fc756a287007d7b4c54c61e1](https://github.com/maragudk/goqite/commit/471f9d49ce356737fc756a287007d7b4c54c61e1)。
- goqite 作者/版权方：Markus Wüstenberg / Maragu ApS。保留原始许可证于 `licenses/goqite-MIT.txt`。
- `internal/lab/goqite.sql` 从该版本 `schema_sqlite.sql` 复制，未修改；文件来源明确，不冒充自写。
- SQLite 驱动：`github.com/mattn/go-sqlite3 v1.14.52`，版权 Yasuhiro Matsumoto，MIT；许可证在 `licenses/go-sqlite3-MIT.txt`。
- 不改动上游模块源码；由 `go.mod`、`go.sum` 固定依赖。Go module checksum 校验不等于完整安全审计。
- 实际使用的是 SQLite；不使用 v0.4.0 的 PostgreSQL 分支，也未验证它。上游 main 后续已有其他改动，本实验不随 main 自动更新。

## 明确新增的内容

`cmd/lab`、`internal/lab/{store,worker,mock,http}.go`、测试、脚本、中文学习资料是此次 AI 辅助补充：订单幂等、事务入队、结果未知、查单、有限退避、迟到结果隔离、本机模拟下游和故障演示。

未导入其他候选项目代码、上游图片或完整 README。未导入用户旧项目或简历中的私密业务数据。项目许可证适用于新增部分；上游部分保留原许可证。

后续每次学习改动建议新增一条记录：日期、本人实际改动、原假设、失败测试、修复后证据、仍不理解的部分。现在不填“本人已掌握”。
