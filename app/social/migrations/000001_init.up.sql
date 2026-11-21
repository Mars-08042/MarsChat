-- social 服务初始表结构。时间列一律为 UTC 的 DATETIME(3)；ID 为 snowflake，用有符号 BIGINT 与 Go int64、proto int64 对应。
-- 状态、角色等列存 social.v1 中对应枚举的数值。

-- 好友关系，一段关系存两行（A→B 与 B→A），在同一事务中插入和删除。
-- “我的好友”是主键前缀扫描：WHERE user_id = ? AND friend_id > ? ORDER BY friend_id
CREATE TABLE friendships (
  user_id    BIGINT      NOT NULL,
  friend_id  BIGINT      NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (user_id, friend_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 好友申请，保留历史：被拒绝后再次申请生成新记录
CREATE TABLE friend_requests (
  id           BIGINT       NOT NULL,
  from_user_id BIGINT       NOT NULL,
  to_user_id   BIGINT       NOT NULL,
  message      VARCHAR(100) NOT NULL DEFAULT '',
  -- 1 待处理、2 已同意、3 已拒绝、4 已失效
  status       TINYINT      NOT NULL,
  created_at   DATETIME(3)  NOT NULL,
  updated_at   DATETIME(3)  NOT NULL,
  handled_at   DATETIME(3)  NULL,
  -- 待处理为 1，其余为 NULL。唯一索引中 NULL 互不冲突，所以同一对用户最多一条待处理申请
  pending_flag TINYINT GENERATED ALWAYS AS (IF(status = 1, 1, NULL)) VIRTUAL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_pending (from_user_id, to_user_id, pending_flag),
  -- 收到的待处理申请：WHERE to_user_id = ? AND status = 1 AND id < ? ORDER BY id DESC
  KEY idx_to (to_user_id, status, id),
  -- 发出的申请：WHERE from_user_id = ? AND id < ? ORDER BY id DESC
  KEY idx_from (from_user_id, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 拉黑，单向关系，不影响好友关系
CREATE TABLE blocks (
  user_id    BIGINT      NOT NULL,
  blocked_id BIGINT      NOT NULL,
  created_at DATETIME(3) NOT NULL,
  PRIMARY KEY (user_id, blocked_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 群资料。GROUPS 是 MySQL 8 的保留字，所以表名为 chat_groups
CREATE TABLE chat_groups (
  id           BIGINT       NOT NULL,
  name         VARCHAR(32)  NOT NULL,
  -- media 服务的文件 ID，步骤 6 之前为空
  avatar       VARCHAR(64)  NOT NULL DEFAULT '',
  notice       VARCHAR(500) NOT NULL DEFAULT '',
  owner_id     BIGINT       NOT NULL,
  -- 1 直接加入、2 需审核
  join_mode    TINYINT      NOT NULL,
  mute_all     TINYINT      NOT NULL DEFAULT 0,
  -- 在锁住群行的事务内维护，与 group_members 的行数一致
  member_count INT          NOT NULL,
  -- 1 正常、2 已解散
  status       TINYINT      NOT NULL DEFAULT 1,
  -- 资料版本号，修改资料时用作乐观锁
  version      INT          NOT NULL DEFAULT 1,
  created_at   DATETIME(3)  NOT NULL,
  updated_at   DATETIME(3)  NOT NULL,
  dismissed_at DATETIME(3)  NULL,
  PRIMARY KEY (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 群成员。退群、被移除直接删除行，历史由事件记录
CREATE TABLE group_members (
  group_id    BIGINT      NOT NULL,
  user_id     BIGINT      NOT NULL,
  -- 1 成员、2 管理员、3 群主
  role        TINYINT     NOT NULL,
  -- 单人禁言截止时间，过期自动失效
  muted_until DATETIME(3) NULL,
  -- 邀请人，自己加入（包括审核通过）为 0
  inviter_id  BIGINT      NOT NULL DEFAULT 0,
  joined_at   DATETIME(3) NOT NULL,
  PRIMARY KEY (group_id, user_id),
  -- 我的群：WHERE user_id = ? AND group_id > ? ORDER BY group_id
  KEY idx_user (user_id, group_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 入群申请，规则与好友申请相同
CREATE TABLE group_join_requests (
  id           BIGINT       NOT NULL,
  group_id     BIGINT       NOT NULL,
  user_id      BIGINT       NOT NULL,
  message      VARCHAR(100) NOT NULL DEFAULT '',
  -- 1 待处理、2 已同意、3 已拒绝、4 已失效
  status       TINYINT      NOT NULL,
  -- 处理人，未处理或自动失效为 0
  handled_by   BIGINT       NOT NULL DEFAULT 0,
  created_at   DATETIME(3)  NOT NULL,
  updated_at   DATETIME(3)  NOT NULL,
  handled_at   DATETIME(3)  NULL,
  pending_flag TINYINT GENERATED ALWAYS AS (IF(status = 1, 1, NULL)) VIRTUAL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_pending (group_id, user_id, pending_flag),
  -- 群的待处理申请：WHERE group_id = ? AND status = 1 AND id < ? ORDER BY id DESC
  KEY idx_group (group_id, status, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 事务 Outbox，结构由 pkg/outbox 定义，各服务同构
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
