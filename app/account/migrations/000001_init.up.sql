-- account 服务初始表结构。时间列一律为 UTC 的 DATETIME(3)；ID 为 snowflake，用有符号 BIGINT 与 Go int64、proto int64 对应。

CREATE TABLE users (
  id            BIGINT       NOT NULL,
  phone         VARCHAR(16)  NOT NULL,
  -- bcrypt 哈希为 60 个字符，留出更换算法的余量
  password_hash VARCHAR(100) NOT NULL,
  nickname      VARCHAR(32)  NOT NULL,
  -- media 服务的文件 ID，步骤 6 之前为空
  avatar        VARCHAR(64)  NOT NULL DEFAULT '',
  -- 0 未设置、1 男、2 女
  gender        TINYINT      NOT NULL DEFAULT 0,
  birthday      DATE         NULL,
  signature     VARCHAR(100) NOT NULL DEFAULT '',
  -- 1 普通用户、2 管理员
  role          TINYINT      NOT NULL DEFAULT 1,
  -- 1 正常、2 封禁
  status        TINYINT      NOT NULL DEFAULT 1,
  created_at    DATETIME(3)  NOT NULL,
  updated_at    DATETIME(3)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_phone (phone)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 一台设备的一次登录
CREATE TABLE sessions (
  id                 BIGINT      NOT NULL,
  user_id            BIGINT      NOT NULL,
  device_id          VARCHAR(64) NOT NULL,
  -- 1 Web、2 Android、3 iOS、4 桌面
  platform           TINYINT     NOT NULL,
  device_name        VARCHAR(64) NOT NULL DEFAULT '',
  -- 当前与上一个刷新令牌随机部分的 SHA-256
  refresh_hash       BINARY(32)  NOT NULL,
  prev_refresh_hash  BINARY(32)  NULL,
  rotated_at         DATETIME(3) NULL,
  refresh_expires_at DATETIME(3) NOT NULL,
  login_ip           VARCHAR(45) NOT NULL DEFAULT '',
  last_ip            VARCHAR(45) NOT NULL DEFAULT '',
  last_active_at     DATETIME(3) NOT NULL,
  revoked_at         DATETIME(3) NULL,
  -- 0 未吊销、1 退出、2 被踢、3 封禁、4 同设备重新登录、5 刷新令牌重放
  revoke_reason      TINYINT     NOT NULL DEFAULT 0,
  created_at         DATETIME(3) NOT NULL,
  -- 有效会话取 device_id，已吊销为 NULL。唯一索引中 NULL 互不冲突，
  -- 所以同一用户同一设备最多一个有效会话，同时保留全部历史记录
  active_device      VARCHAR(64) GENERATED ALWAYS AS (IF(revoked_at IS NULL, device_id, NULL)) VIRTUAL,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_active_device (user_id, active_device),
  -- 设备列表与封禁时吊销全部会话：WHERE user_id = ? AND revoked_at IS NULL
  KEY idx_user_revoked (user_id, revoked_at)
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
