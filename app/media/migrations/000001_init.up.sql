-- media 服务初始表结构。时间列一律为 UTC 的 DATETIME(3)；ID 为 snowflake，用有符号 BIGINT 与 Go int64、proto int64 对应。
-- 用途、种类等列存 media.v1 中对应枚举的数值。

-- 文件记录，写入后不再修改。查询只有按主键（单个与 IN 批量），不需要二级索引。
-- 对象键由 ID 推出（原文件 files/{id}、缩略图 thumbs/{id}.jpg），不单独存储。
CREATE TABLE files (
  id             BIGINT       NOT NULL,
  uploader_id    BIGINT       NOT NULL,
  -- 1 聊天、2 头像
  purpose        TINYINT      NOT NULL,
  -- 1 普通文件、2 图片（已生成缩略图）
  kind           TINYINT      NOT NULL,
  -- 清理后的文件名，只用于展示和下载时的默认文件名
  name           VARCHAR(255) NOT NULL,
  size           BIGINT       NOT NULL,
  -- 按文件内容识别的类型
  mime_type      VARCHAR(127) NOT NULL,
  sha256         BINARY(32)   NOT NULL,
  -- 图片原始宽高，普通文件为 0
  width          INT          NOT NULL DEFAULT 0,
  height         INT          NOT NULL DEFAULT 0,
  -- 缩略图字节数，没有缩略图为 0
  thumbnail_size INT          NOT NULL DEFAULT 0,
  -- 访问密钥（128 位随机数），凭它换取聊天文件的下载地址
  access_key     BINARY(16)   NOT NULL,
  -- 写入时的存储：1 local、2 oss，用于排查切换存储后的问题
  storage        TINYINT      NOT NULL,
  created_at     DATETIME(3)  NOT NULL,
  PRIMARY KEY (id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;
