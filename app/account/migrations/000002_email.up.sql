-- 账号标识从手机号改为邮箱（见 docs/design/email-verification.md）。
-- 已有的开发数据填占位邮箱 u{id}@legacy.invalid：.invalid 是保留顶级域，发送方会拒绝向它发信；
-- 这些账号仍可以用原密码登录，但收不到验证码。
-- 254 是邮箱地址的最大长度；排序规则 utf8mb4_0900_ai_ci 不区分大小写，服务端另外统一转为小写。

ALTER TABLE users ADD COLUMN email VARCHAR(254) NULL AFTER id;

UPDATE users SET email = CONCAT('u', id, '@legacy.invalid');

ALTER TABLE users
  MODIFY email VARCHAR(254) NOT NULL,
  ADD UNIQUE KEY uk_email (email),
  DROP INDEX uk_phone,
  DROP COLUMN phone;
