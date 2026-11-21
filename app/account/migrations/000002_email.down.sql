-- 回滚为手机号。原手机号无法还原，用 p{id 的十六进制} 填充（不是合法手机号，只为满足非空与唯一约束，
-- snowflake ID 小于 2^60 时不超过 16 个字符）。邮箱随列一起删除。

ALTER TABLE users ADD COLUMN phone VARCHAR(16) NULL AFTER id;

UPDATE users SET phone = CONCAT('p', CONV(id, 10, 16));

ALTER TABLE users
  MODIFY phone VARCHAR(16) NOT NULL,
  ADD UNIQUE KEY uk_phone (phone),
  DROP INDEX uk_email,
  DROP COLUMN email;
