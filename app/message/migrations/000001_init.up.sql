-- message 服务初始表结构。时间列一律为 UTC 的 DATETIME(3)；ID 为 snowflake，用有符号 BIGINT 与 Go int64、proto int64 对应。
-- 读扩散：消息按会话只存一份，会话内 seq 连续递增；每个成员只记录自己的已读位置（ADR-0006）。
-- 主键与索引都以会话 ID 开头，将来可以按会话分片。

-- 会话。群聊的 ID 就是群 ID；单聊的 ID 是新的 snowflake，按两个用户 ID 唯一。
CREATE TABLE conversations (
  id         BIGINT      NOT NULL,
  -- 1 单聊、2 群聊，与 message.v1.ConversationType 一致
  type       TINYINT     NOT NULL,
  -- 仅单聊：两人中较小、较大的用户 ID。群聊为 NULL，唯一索引中 NULL 互不冲突
  user_low   BIGINT      NULL,
  user_high  BIGINT      NULL,
  -- 会话最新消息的 seq，在锁住本行的事务内递增
  max_seq    BIGINT      NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_pair (user_low, user_high)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 会话成员与已读位置。单聊在第一条消息时插入两行；群聊由 social 的群事件维护。
CREATE TABLE conversation_members (
  conversation_id BIGINT      NOT NULL,
  user_id         BIGINT      NOT NULL,
  -- 已读位置，只增不减
  read_seq        BIGINT      NOT NULL DEFAULT 0,
  joined_at       DATETIME(3) NOT NULL,
  updated_at      DATETIME(3) NOT NULL,
  PRIMARY KEY (conversation_id, user_id),
  -- 我的会话：WHERE user_id = ? AND conversation_id > ? ORDER BY conversation_id
  KEY idx_user (user_id, conversation_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 消息。历史（seq < ? 降序）和补拉（seq > ? 升序）都是主键范围扫描。
CREATE TABLE messages (
  conversation_id BIGINT      NOT NULL,
  seq             BIGINT      NOT NULL,
  sender_id       BIGINT      NOT NULL,
  -- 发送方生成的幂等键，只含字母、数字、-、_，用 ascii 字符集缩小索引
  client_msg_id   VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  -- message.v1.MessageContent 的 protobuf 编码
  content         BLOB        NOT NULL,
  created_at      DATETIME(3) NOT NULL,
  PRIMARY KEY (conversation_id, seq),
  -- 幂等：同一发送者在同一会话中的 client_msg_id 只能出现一次。以会话 ID 开头，分片后仍然有效
  UNIQUE KEY uk_client (conversation_id, sender_id, client_msg_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 事务 Outbox，与其他服务同构，见 pkg/outbox。
CREATE TABLE outbox_events (
  id           BIGINT       NOT NULL AUTO_INCREMENT,
  topic        VARCHAR(128) NOT NULL,
  event_key    VARCHAR(64)  NOT NULL,
  event_type   VARCHAR(128) NOT NULL,
  payload      BLOB         NOT NULL,
  created_at   DATETIME(3)  NOT NULL,
  published_at DATETIME(3)  NULL,
  PRIMARY KEY (id),
  KEY idx_unpublished (published_at, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;
