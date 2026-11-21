# go-redis v9 默认用 RESP3：BF.EXISTS 返回布尔值，按整数读取会让布隆过滤器静默失效

- 涉及：[pkg/bloom/bloom.go](../../pkg/bloom/bloom.go)（原 `app/account/internal/data/user_bloom.go`）、[test/e2e/bloom_test.go](../../test/e2e/bloom_test.go)；go-redis `v9.23.0` 的 `command.go`（`Cmd.Int64`、`Cmd.Bool`）、`options.go`（`Protocol`）
- 发现时间：2026-11-19，重构步骤 3 补 TD-27 的布隆过滤器集成测试时

## 现象

新写的集成测试里，过滤器重建完成后查询一个不存在的 ID，`MightExist` 仍返回 `true`；测试辅助函数直接读 `BF.EXISTS` 时报错：

```
redis: unexpected type=bool for Int64
```

account 的用户 ID 过滤器从步骤 2 起就是这段代码。功能上一直正常：判断“可能存在”之后会回源查库，查不到照样返回 404，e2e 的“查询不存在的用户”也就通过了。但过滤器没有拦截过任何 ID，防穿透没有生效，日志里也没有任何提示。

## 根因

- go-redis v9 的 `Options.Protocol` 默认为 3，连接时用 `HELLO 3` 协商 RESP3。
- RESP3 有原生布尔类型，RedisBloom 的 `BF.EXISTS` 在 RESP3 下返回布尔值，在 RESP2 下返回整数 0/1。
- 原代码 `pipe.Do(ctx, "BF.EXISTS", ...)` 拿到的是通用 `*redis.Cmd`，用 `Int64()` 读取。值是 `bool` 时 `Int64()` 返回类型错误。
- 出错时代码按“过滤器不可用、放行”处理，并且只记 DEBUG 日志。“出错就放行”本身是对的（过滤器故障不能把真实用户判为不存在），但它把一个必然发生的类型错误也当作偶发故障吞掉了。

`Do` 返回的通用 `Cmd` 不带类型信息，读取方法要自己选；go-redis 为常用命令提供的类型化方法（`Exists`、`SetNX` 等）不受影响。

## 解决

- 改用 `Cmd.Bool()`：它同时接受 `bool`（RESP3）和 `int64`（RESP2），两种协议都能解析。
- 解析失败仍然放行，但改记 WARN。这类错误一旦出现就是每次都出现，必须在日志里看得见。
- 集成测试的辅助函数同样改用 `Bool()`。

## 验证

- `test/e2e/bloom_test.go` 的 `TestBloomRebuildAndLookup`：重建完成后，存在的 ID 返回 `true`，不存在的返回 `false`。修复前这条断言失败。
- 其余 6 个失败分支测试（重试、脏标记、多轮重建、锁、Redis 不可用）全部通过。
- 检查了仓库里其他按原始回复读取的地方：所有 Lua 脚本都返回整数或整数数组（`DEL`、`PEXPIRE` 的结果或字面量），两种协议下类型一致，不受影响。

## 预防

- 用 `rdb.Do` 执行模块命令或通用命令时，先查清它在 RESP3 下的回复类型。布尔语义的回复用 `Bool()` 读取，不要用 `Int64()`。
- Lua 脚本返回 `true`/`false` 时，RESP3 下也是布尔值；脚本里统一返回整数。
- “出错就放行”的降级路径要能被发现：至少记 WARN，并且要有测试证明正常路径确实走到了“拦截”的分支，而不只是“结果正确”。
- miniredis 不支持模块命令，相关逻辑要有连真实 Redis 的测试（`test/e2e`）。
