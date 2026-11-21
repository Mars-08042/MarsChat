# 注册事务中查不到行的 SELECT ... FOR UPDATE 锁住索引末尾的间隙，并发注册互相死锁

- 涉及：[app/account/internal/biz/auth.go](../../app/account/internal/biz/auth.go) 的 `Register`、`startSession`、`createSession`；[app/account/internal/data/session.go](../../app/account/internal/data/session.go) 的 `RevokeActiveByDevice`；`sessions` 表的唯一索引 `uk_user_active_device (user_id, active_device)`
- 发现时间：2026-11-21，重构步骤 8 压测准备数据（k6 经网关并发注册）

## 现象

k6 一次并发注册 4 个用户，3 个返回 `500 INTERNAL`。account 日志：

```
SQL executed ... "sql":"INSERT INTO `sessions` (...) VALUES (...)", "error":"Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction"
```

逐个注册从来没有问题，e2e 和集群测试也都是串行注册，所以一直没暴露。

## 根因

- 注册事务先插入用户，再调用 `startSession`。`startSession` 为了保证“同一设备只保留一个有效会话”，先用 `SELECT id FROM sessions WHERE user_id = ? AND active_device = ? FOR UPDATE` 锁住该设备上的旧会话，再插入新会话。
- 新用户不可能有会话，查询查不到行。在可重复读隔离级别下，InnoDB 对“唯一索引等值查询查不到”的情况加的是**间隙锁**，锁住这个键本该所在的间隙。
- 用户 ID 由发号器递增生成，新用户的 `(user_id, device_id)` 总是落在索引末尾，也就是最大记录到 supremum 之间的同一个间隙。
- 间隙锁之间互相兼容，并发的几个注册事务都能拿到它；随后各自插入会话，插入要申请同一间隙上的插入意向锁，与对方持有的间隙锁冲突，于是互相等待，InnoDB 检测到死锁，回滚其中一方。

## 解决

- 把 `startSession` 拆成两步：`startSession` 吊销设备上的旧会话后调用 `createSession`；注册直接调用 `createSession`，不做加锁查询。新用户没有会话，跳过这一步不影响“同一设备只保留一个会话”。
- 登录仍然走 `startSession`：老用户的会话记录把索引分隔成各自的小间隙，并发登录落进同一间隙的概率很低。剩余的风险记为 [TD-43](../plans/tech-debt.md)。

## 验证

- 修复后 k6 一次并发注册 40 个、再一次 1000 个（每批 40 个并发）全部成功，1000 个用户经网关用时约 31 秒。
- 新增 e2e 用例 `TestConcurrentRegister`：8 个注册请求同时发出，全部成功。

## 预防

- 事务里“先加锁查询，查不到再插入”的写法，在查不到行时锁的是间隙。插入的键单调递增（雪花 ID、自增 ID、时间戳）时，所有并发事务都落在索引末尾的同一个间隙，几乎必然死锁。能确定不存在时就不要查；必须查时，考虑先插入、靠唯一约束兜底，或者对 1213 重试整个事务。
- 只靠串行的功能测试发现不了这类问题，写路径要有并发用例。压测准备数据就是一次天然的并发测试。
