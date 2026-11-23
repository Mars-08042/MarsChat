-- 管理员按状态列出用户（见 docs/design/account-service.md 第 9 节）：
-- 封禁的用户很少，没有这个索引时按状态查一页或计数都要扫全表。
ALTER TABLE users ADD KEY idx_status_id (status, id);
