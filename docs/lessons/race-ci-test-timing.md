# 本地稳定通过的测试在 CI 的 `-race` 下失败

- 涉及：[mail_test.go](../../app/account/internal/data/mail_test.go)、[hub_test.go](../../app/access/internal/biz/hub_test.go)、[call_test.go](../../app/access/internal/biz/call_test.go)
- 发现时间：2026-11-21，`dev` 第一次合并进 `main` 后 CI 的「单元测试」步骤（`go test -race ./...`）

## 现象

- `app/account/internal/data`：`TestSMTPSenderDeadline` 报 `DATA RACE`。测试在假 SMTP 服务器启动后写 `silent`，服务器的连接协程读它。
- `app/access/internal/biz`：`TestCallReject`、`TestCallAlreadyAnswered` 在准备阶段等来电时 `read: i/o timeout`。
- 本地 `go test ./...` 和收尾时的 `go test -race ./...` 都通过；本地用 `-race -count=20` 反复运行，又发现 `TestCallRemoteCallee`、`TestCallSignalPeerUnreachable` 偶尔失败（后者用原始测试文件运行 50 次失败 9 次）。

## 根因

两类问题，都在测试代码里，业务代码的顺序是有意设计的：

1. **竞争检测器看不到经 TCP 传递的先后关系**。测试先赋值、再连接，服务器接受连接后才读这个字段，实际顺序没有问题；但这个先后关系只经过内核的 socket，Go 内存模型不承认，`-race` 按数据竞争报告。只要竞争检测器碰巧观察到这对读写就会报，和机器快慢有关。
2. **服务端先回复、再推送，测试收到回复就当后续动作已经完成**：
   - 认证按「登记到 Hub → 回复 `AuthOk` → 写在线路由」（[conn.go](../../app/access/internal/biz/conn.go) 的 `handleAuth`），保证推送不早于 `AuthOk`。客户端收到 `AuthOk` 时路由可能还没写完，紧接着的呼叫找不到这个设备，它收不到来电。
   - 邀请和挂断先回复 `CallAck`，再推送来电或结束事件（保证主叫先拿到 `call_id`）。测试收到 `CallAck` 就读转发记录，或者马上让转发失败，可能早于推送。

CI 的机器更慢，`-race` 又放大了协程调度的间隔，这些窗口更容易被撞上。

## 解决

- 假 SMTP 服务器启动后还会被修改的 `authCode`、`silent` 改由 `mu` 保护，测试通过 `setAuthCode`、`setSilent` 修改。
- 测试的 `login` 收到 `AuthOk` 后等路由写入再返回。
- `TestCallRemoteCallee` 等转发数到 2 再检查；`TestCallSignalPeerUnreachable` 等来电转发完再让实例不可达。
- 变异验证：只给被叫第二个设备的路由写入加 100 毫秒延迟，修复前稳定复现 CI 的超时，修复后通过；给结束事件的转发加 50 毫秒延迟，修复后的 `TestCallRemoteCallee` 通过。修复后 access 包 `-race -count=30`、全部包 `-race` 通过。

## 预防

- 测试里在服务端协程启动后再修改的共享字段，一律加锁或用原子类型，不要依赖「先赋值、后连接」。
- 服务端有「先回复、再做后续动作」的顺序时，测试断言后续动作的结果要用 `eventually` 等待，不能把回复当作完成信号。
- 改了并发相关的测试后，在本地用 `go test -race -count=20` 跑一遍对应的包，再提交。
