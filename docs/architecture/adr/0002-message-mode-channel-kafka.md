# ADR-0002 消息中转支持 channel / kafka 两种模式

- 状态：已采纳（追溯记录）
- 日期：2026-11-07 整理

## 背景

所有聊天消息都由一个转发循环串行处理。连接读取协程与转发循环之间需要一个缓冲层，避免消息突发时读取协程被阻塞。

依据：`configs/config.toml` 中 `messageMode = "channel" # 消息模式 channel or kafka`；`internal/service/chat/client.go` 中通道写满时的注释“否则考虑加宽 channel size，或者使用 kafka”。

## 候选方案

1. **channel 模式**：进程内 Go 通道 `ChatServer.Transmit`（容量 100），满时暂存到每个连接的 `SendTo` 通道，再满则提示用户稍后重试。
2. **kafka 模式**：读取协程把消息写入 topic `chat_message`，`KafkaChatServer` 以消费组 `chat` 读取后处理。

## 决策

两种模式并存，由配置 `kafkaConfig.messageMode` 选择，默认 `channel`。处理逻辑（落库、计算接收方、推送）在 `server.go` 和 `kafka_server.go` 中各实现一份。

## 原因

从代码注释看，kafka 模式被定位为 channel 缓冲不足时的扩展手段。没有找到更详细的选型记录。

## 代价与风险

- 处理逻辑重复两份，修改时容易只改一处（TD-14），kafka 模式已出现登出无效等遗漏（TD-05）。
- kafka 模式下在线连接表仍在进程内存中，并不能支撑多实例部署（TD-07）。
- Writer 配置为 `RequireNone`，不等待 broker 确认，没有提高投递可靠性。
- kafka 模式额外依赖 Kafka（及 Zookeeper）运维，topic 需要手动预建。

## 适用条件

- 单实例、低并发场景使用 channel 模式即可。
- 若目标是多实例或可靠投递，需要先解决在线路由和确认机制，届时应重新评估两种模式是否都需要保留，并新写 ADR。
