# franz-go 按记录的 Timestamp 计算投递超时，填事件创建时间会让积压事件一发出就超时

- 涉及：[pkg/outbox/publisher.go](../../pkg/outbox/publisher.go) 的 `toRecords`；franz-go `v1.22.3` 的 `pkg/kgo/sink.go`（`recBatch.isTimedOut`）、`config.go`（`RecordDeliveryTimeout`）
- 发现时间：2026-11-19，重构步骤 4 第一次启动 Outbox 发布器时

## 现象

account 启动后，发布器拿到租约，第一批 21 条事件发送失败。之后按退避每次重试都失败：

```
produce outbox events failed, will retry ... error="produce 21 records: records have timed out before they were able to be produced"
```

生产者配置的 `RecordDeliveryTimeout` 是 30 秒，但从拿到租约到报错只过了约 20 毫秒。Kafka 本身正常：同一时间用新写入的记录做的收发测试是通过的。

## 根因

- 发布器把 Outbox 事件的创建时间填进了 `kgo.Record.Timestamp`，想让 Kafka 记录带上事件的发生时间。
- franz-go 判断一个批次是否超时用的是 `time.Since(b.records[0].Timestamp) > recordTimeout`，以批次中**第一条记录的 Timestamp** 作为起点，而不是记录进入生产者缓冲区的时刻。只有调用方没填 Timestamp 时，`produce` 才会填上 `time.Now()`。
- account 积压的是步骤 2、3 留下的事件，创建时间早已超过 30 秒，所以每一批在写请求之前就被判定超时；同一分区缓冲中的其他记录也一起失败（franz-go 为保证顺序无空洞的行为）。
- 新写入的事件不受影响，所以只在“有积压”时出现：服务长时间停机、Kafka 故障恢复后，正是最需要发布器补发的时候。

## 解决

- 不设置 `Record.Timestamp`，由 franz-go 填发送时刻。事件发生的时间本来就在各事件的 payload 里（`created_at`、`revoked_at` 等），消费方不依赖 Kafka 记录的时间戳。
- 记录的构造抽成 `toRecords`，单元测试 `TestToRecords` 断言时间戳为空。

## 验证

- 修复后重启 account、social，积压的 22 条和 64 条事件在启动时全部发出，`outbox_events` 中没有未发布的行。
- `test/e2e/kafka_test.go` 的 `TestOutboxPublishesServiceEvents` 通过：social 的好友申请和 account 的退出事件都能从 Kafka 消费到。

## 预防

- 用 franz-go 生产时，`Record.Timestamp` 只在确实需要 Kafka 时间戳语义（例如按时间检索、按时间戳保留）时设置，并且要保证它接近当前时间；否则留空。
- 投递超时类的配置，要用“很早以前就写入、现在才发”的数据测一遍，而不只是用刚生成的数据。Outbox、重放、补发这类场景天然会遇到旧数据。
