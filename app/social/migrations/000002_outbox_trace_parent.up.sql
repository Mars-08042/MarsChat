-- Outbox 事件记录写入时所在 Span 的 W3C traceparent（55 个字符），发布时放进 Kafka 记录头，
-- 消费者接着同一条 Trace（见 docs/design/observability.md 3.3）。没有 Span 的事件为空串。
-- 带默认值的加列在 MySQL 8.0.29+ 默认用 INSTANT 算法，只改元数据，不重建表。

ALTER TABLE outbox_events ADD COLUMN trace_parent VARCHAR(64) NOT NULL DEFAULT '' AFTER payload;
