# 设计：通话的 TURN 中继与临时凭据

- 状态：已采纳（2026-11-22） | 日期：2026-11-22 | 关联：[call-signaling.md](call-signaling.md)（第 1 节「不在本步骤范围内」、第 8 节问题 7）、[前端设计](frontend.md) 3.9.5、[web-deployment.md](web-deployment.md)、[前端计划](../plans/completed/frontend-rewrite.md) 第 9 阶段

开发者已确认（2026-11-22）：前端第 8 阶段先不部署 coturn，放到第 9 阶段与部署方式一起实施。本文先以提议提交，确认结果与实施中的调整见第 8、9 节；第 3 节已按实施结果更新。

## 1. 背景与目标

前端第 8 阶段接入了通话，`RTCPeerConnection` 的 `iceServers` 为空，只能收集本机地址（host 候选）。

- 同一台机器、同一局域网内可以直连，已在浏览器里验证（计划第 8 阶段）；
- 双方隔着 NAT（例如手机 4G 打给家里宽带）时，大多数情况下没有可用的候选对，15 秒后按「网络无法直连」结束；
- 一些浏览器扩展或企业策略把 WebRTC 的 IP 策略设为只走代理（`disable_non_proxied_udp`），这时连 host 候选都没有，只有 TURN 能连通。

目标：

- 部署 coturn，提供 STUN 和 TURN（UDP、TCP，部署时加 TLS）；
- 由后端给通话双方下发短期有效的 TURN 凭据，凭据只发给正在通话的用户，不能长期复用；
- 没有配置 TURN 时行为与现在一致，开发环境不强制依赖 coturn。

不在范围内：群通话（需要 SFU，另行设计）、媒体录制。

## 2. 需求与约束

- **凭据**：用 coturn 的 TURN REST API 机制（`use-auth-secret` + `static-auth-secret`）。coturn 示例配置中的约定：用户名为 `timestamp:userid`（分隔符默认 `:`），密码为 `base64(hmac(secret key, usercombo))`。时间戳是凭据的过期时刻（Unix 秒）。哈希算法是 HMAC-SHA1：access 签发的凭据被 coturn 4.19 接受，错误密钥被拒绝（第 9 节）。
- **共享密钥**：access 与 coturn 共用一个密钥，放在被忽略的 `.env` 中，不进仓库。
- **防滥用**：TURN 能把流量转发到任意地址，必须禁止转发到内网和本机地址（`denied-peer-ip`），否则可以被用来访问服务器所在网络；限制每个用户和全局的分配数（`user-quota`、`total-quota`）。
- **端口约定**：容器宿主机端口为默认端口 + 10000，默认只绑定 `127.0.0.1`（AGENTS.md）。TURN 的中继端口要能被浏览器直接访问：监听端口为 13478，中继端口段 49160～49200 按相同端口号映射；局域网访问时绑定地址改成本机的局域网地址，公网部署的做法见第 6 节。
- **资源**：新增一个 coturn 容器；不新增 Go 依赖（HMAC 与 Base64 用标准库）。

## 3. 方案

### 3.1 coturn 容器

Compose 新增 `coturn` 服务，放在单独的 profile `call`（`make call-up`、`make call-down`），默认不启动，与监控组件的做法一致。镜像 `coturn/coturn:4.19.2-alpine3.24`，按标签加 digest 锁定，以 `nobody` 运行。

固定配置在 `deploy/coturn/turnserver.conf`：

| 配置 | 值 | 说明 |
|---|---|---|
| `listening-port` | 3478 | 宿主机映射为 13478（UDP、TCP） |
| `min-port`、`max-port` | 49160～49200 | 本地只开 41 个中继端口，按相同端口号映射；Docker 按端口逐个转发，大段 UDP 端口映射启动很慢 |
| `use-auth-secret`、`realm=marschat`、`fingerprint` | | TURN REST API |
| `no-tls` | | 没有证书，不开 TLS 监听；DTLS 默认关闭 |
| `no-multicast-peers`、`denied-peer-ip` | 0.0.0.0/8、10/8、100.64/10、169.254/16、172.16/12、192.168/16、fc00::/7、fe80::/10 | 禁止中继到内网；回环地址 coturn 默认就禁止 |
| `user-quota`、`total-quota` | 10、200 | 每个临时用户名的并发分配上限、全局上限 |
| `log-file=stdout`、`simple-log`、`pidfile=/tmp/turnserver.pid` | | 日志由 `docker logs` 查看 |

随部署环境变化的参数由 `compose.yaml` 的启动参数传入：

- `--static-auth-secret`：`.env` 的 `MARSCHAT_TURN_SECRET`，`make keys` 生成（32 字节随机数的 base64url，作为命令行参数不需要转义）；
- `--external-ip=<对外地址>/172.30.0.12`：coturn 在容器里只看到容器地址（固定 IP `172.30.0.12`），告诉客户端的中继地址要换成对外地址。对外地址是 `MARSCHAT_TURN_EXTERNAL_IP`，默认 `127.0.0.1`，局域网访问时设为本机的局域网地址；
- `--allowed-peer-ip=<对外地址>`：两端都走中继时，对端地址就是 coturn 自己的对外地址（局域网地址落在禁止的 192.168/16 中），单独放行这一个地址；
- 端口绑定地址是 `MARSCHAT_TURN_BIND_IP`，默认 `127.0.0.1`，本机或局域网部署时与对外地址相同。

密钥不能在 Compose 里写成 `${...:?}` 必填：Compose 不分 profile 插值整个文件，没配置 TURN 的人 `make up` 也会失败。改由 `make call-up` 检查 `.env` 中是否有密钥。

### 3.2 凭据下发：随 CallAck 返回

邀请和接听的 `CallAck` 新增 ICE 服务器列表，其他请求（挂断、信令）的 `CallAck` 不带：

```proto
message CallAck {
  int64 call_id = 1;
  // 邀请和接听成功时返回；没有配置时为空，客户端只用本机地址
  repeated IceServer ice_servers = 2;
}

// 对应 RTCIceServer
message IceServer {
  repeated string urls = 1;  // 例如 turn:turn.example.com:3478?transport=udp
  string username = 2;       // <过期时刻 Unix 秒>:<用户 ID>
  string credential = 3;     // base64(HMAC-SHA1(secret, username))
}
```

时机正好：主叫在收到 `CallAccepted` 后、被叫在收到接听的 `CallAck` 后才创建 `RTCPeerConnection`（前端设计 3.9.5），这时两边都已拿到凭据，不需要额外的请求。凭据里是各自的用户 ID，两端的用户名不同。

access 配置新增（`app/access/configs/config.yaml`，代码在 `biz/turn.go`）：

```yaml
call:
  turn:
    # 为空时不下发 ICE 服务器；多个地址用逗号分隔
    urls: ${TURN_URLS:}
    secret: ${TURN_SECRET:}
    # 12 小时；protojson 的时长只能写成秒
    ttl: 43200s
```

`urls` 是逗号分隔的字符串而不是列表：Kratos 的环境变量只能整体替换一个字符串值，用一个 `MARSCHAT_TURN_URLS` 传入全部地址最简单。校验规则：每一项以 `stun:`、`stuns:`、`turn:`、`turns:` 开头；填了地址就必须有密钥；`ttl` 在 1 分钟到 48 小时之间。Compose 把 `.env` 的 `MARSCHAT_TURN_URLS`、`MARSCHAT_TURN_SECRET` 传给 access。

### 3.3 前端

- 引擎为每通通话保存 `CallAck` 里的 ICE 服务器（邀请和接听的回复），创建 `RTCPeerConnection` 时使用，不再固定为空数组；
- 排查 TURN 部署时，在浏览器控制台执行 `localStorage.setItem("marschat.debug.ice-relay", "1")`，之后的通话用 `iceTransportPolicy: "relay"` 只走中继，连不通说明 coturn 或凭据有问题。开关只影响本机这个浏览器，开发和生产构建都可用（原提议只在开发环境启用，改动原因见第 9 节）；
- 文案不变：TURN 也连不通时仍按「网络无法直连」结束。

## 4. 接口、配置与数据变化

- `api/access/v1/call.proto`：`CallAck` 新增 `ice_servers`，新增 `IceServer`。只加字段，`buf breaking` 通过；`data-flow.md` 第 9 节同步。
- access：读取 `call.turn` 配置，生成凭据；`deployment.md` 补配置项。
- Compose：新增 `coturn` 服务与 profile `call`；Makefile 新增 `call-up`、`call-down`；`make keys` 生成 `MARSCHAT_TURN_SECRET`；`deployment.md` 补端口与启动方式。
- 不涉及数据库和 Redis。

## 5. 候选方案与权衡

| 问题 | 推荐 | 备选与不采用的原因 |
|---|---|---|
| 凭据从哪里下发 | 邀请、接听的 `CallAck` | 新增 HTTP 接口 `GET /v1/calls/ice-servers`：多一次请求，而 access 对外只有 `/ws` 入口，要么给 access 加业务 HTTP 接口，要么把 TURN 密钥交给另一个服务。放在 `AuthOk`：每个连接都拿到凭据，大多数用户根本不打电话，有效期还要覆盖整个连接的生命周期 |
| 鉴权方式 | TURN REST API（共享密钥，无状态） | 长期凭据写进 coturn 数据库：要同步用户，凭据泄露后长期可用 |
| 有效期 | 12 小时，可配置 | 很短（例如 10 分钟）：续期分配时凭据已过期，通话超过有效期中继会断开；要支持就得在通话中下发新凭据并 ICE 重启，复杂度不值得 |
| 网络模式（公网） | 第 9 阶段与部署方式一起定 | 见第 6 节 |

## 6. 风险与验证

| 风险 | 处理 |
|---|---|
| Docker 映射大段 UDP 端口很慢，公网部署时中继端口需要几百上千个 | 公网部署常用 `network_mode: host` 并设置 `external-ip`；第 9 阶段设计部署时决定 |
| coturn 在容器里看到的是容器地址 | 中继端口按相同端口号映射，并用 `external-ip=<对外地址>/172.30.0.12` 告诉 coturn 对外地址；局域网地址已用 relay-only 验证（第 9 节） |
| 默认的对外地址 `127.0.0.1` 是回环地址，coturn 默认禁止中继到回环地址 | 两端都在本机时本来就能直连，不需要 TURN；要验证中继时按局域网的做法把对外地址设成局域网地址 |
| 凭据被截获后被别人用来中继流量 | 有效期有限；每用户分配数上限；禁止中继到内网；凭据只经过已认证的 WebSocket 下发 |
| 屏蔽 UDP 的网络 | TURN over TCP（3478）先覆盖一部分；TLS（5349 或 443）需要证书，随部署实施 |

验证：

- 单元测试：凭据的用户名格式、过期时刻、HMAC 结果（与 coturn 文档或 `turnutils` 的计算结果对照）；未配置时不下发；
- e2e：邀请与接听的 `CallAck` 带 ICE 服务器，信令的 `CallAck` 不带；
- 浏览器：开发开关打开 relay-only，两个浏览器互相呼叫，`getStats` 中选中的候选对两端都是 `relay`；凭据过期后 coturn 拒绝分配；
- 中继到 `127.0.0.1`、`192.168.x.x` 的请求被拒绝。

## 7. 待确认问题

1. **下发方式**：推荐随邀请、接听的 `CallAck` 返回（proto 只加字段）。
2. **有效期**：推荐 12 小时，可配置。
3. **部署**：coturn 的网络模式、公网地址、TLS 证书随第 9 阶段的部署设计一起确认。
4. **TURN 的地址**：是否与网关使用同一个域名（例如 `turn:<域名>:3478`），还是单独的子域名。

## 8. 确认结果（2026-11-22）

开发者确认按提议实施：

1. 随邀请、接听的 `CallAck` 下发；
2. 有效期 12 小时，可配置；
3. 部署方式随 [web-deployment.md](web-deployment.md)：这次只做本机与局域网（Compose 端口映射、`external-ip`），公网的主机网络与 TLS 留给公网部署；
4. 公网部署时 TURN 与网关用同一个域名（web-deployment.md 4.3）。

## 9. 实施中的调整与验证

调整：

| 项目 | 原提议 | 实施 | 原因 |
|---|---|---|---|
| `urls` 配置 | 列表 | 逗号分隔的字符串 | 用一个环境变量整体传入（3.2） |
| `ttl` 写法 | `12h` | `43200s` | protojson 的 Duration 只接受秒，配置测试发现 |
| `user-quota` | 4 | 10 | 设成 4 时 coturn 自带的 `turnutils_uclient` 第一次认证分配就收到 486（Allocation Quota Reached），设成 10 正常。浏览器对每个 TURN 地址、在每块本机网卡上各申请一次分配，装了 VPN 或虚拟网卡的电脑一通通话就能用到 6～8 个 |
| `realm` | `marschat.local` | `marschat` | 只是认证域的名字，不对应真实域名，去掉容易误会的后缀 |
| 回环与内网 | `denied-peer-ip` 包含 127/8 与 `::1` | 回环交给 coturn 默认的禁止；增加 100.64/10；`--allowed-peer-ip` 放行对外地址 | 两端都走中继时对端是 coturn 自己的对外地址 |
| 只走中继的开关 | 只在开发环境 | 开发和生产构建都可用（`localStorage`） | 部署的人要在生产构建里排查 TURN；开关只影响自己的浏览器 |
| 密钥必填检查 | — | 由 `make call-up` 检查 | Compose 不分 profile 插值，`:?` 会让所有人的 `make up` 失败 |

验证（2026-11-22，容器模式，对外地址为本机的局域网地址，变量只在命令行传入，验证后恢复默认）：

- 单元测试：凭据的用户名、过期时刻和密码（期望值用 Python 的 `hmac`、`hashlib.sha1` 独立算出）；未配置时不下发；邀请与接听的 `CallAck` 带 ICE 服务器、用户名里是各自的用户 ID，信令和挂断的不带；配置校验（缺密钥、错误前缀、空项、过短的有效期）；前端引擎使用 `CallAck` 里的服务器、开关打开时 `iceTransportPolicy` 为 `relay`；
- coturn：`turnutils_uclient -y`（两个客户端互相经中继收发）经宿主机端口映射和局域网地址收发 40 条，没有丢包；错误密钥被拒绝（`message integrity does not match any auth secret`）；中继到 `127.0.0.1`、同一局域网的其他地址、`10.0.0.5`、Docker 网络地址都返回 403，公网地址放行；
- 浏览器：两个用户都打开只走中继的开关后呼叫，两端选中的候选对都是 `relay`↔`relay`（UDP），`RTCPeerConnection` 的配置里有两个 TURN 地址，用户名分别是各自的用户 ID，凭据 11.99 小时后过期；被叫收到主叫的 1456 个音频包，候选对双向都有流量；挂断后两端连接关闭；
- 未验证：凭据过期后 coturn 拒绝续期（要等 12 小时，或者把有效期调到 1 分钟再测）；跨网络（手机 4G 打给家里宽带）；TURN over TCP 与 TLS；被叫一侧的音频由页面合成，测试页面没有用户操作，合成的音源没有启动，所以只验证了主叫到被叫方向的媒体包。

## 参考

- coturn 示例配置中的选项说明与 TURN REST API 约定：<https://github.com/coturn/coturn/blob/master/examples/etc/turnserver.conf>
