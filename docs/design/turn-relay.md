# 设计：通话的 TURN 中继与临时凭据

- 状态：提议中 | 日期：2026-11-22 | 关联：[call-signaling.md](call-signaling.md)（第 1 节「不在本步骤范围内」、第 8 节问题 7）、[前端设计](frontend.md) 3.9.5、[前端计划](../plans/active/frontend-rewrite.md) 第 9 阶段

开发者已确认（2026-11-22）：前端第 8 阶段先不部署 coturn，放到第 9 阶段与部署方式一起实施。本文是供审阅的提议，确认前不改 Compose、proto 和服务代码。

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

- **凭据**：用 coturn 的 TURN REST API 机制（`use-auth-secret` + `static-auth-secret`）。coturn 示例配置中的约定：用户名为 `timestamp:userid`（分隔符默认 `:`），密码为 `base64(hmac(secret key, usercombo))`。时间戳是凭据的过期时刻（Unix 秒）。示例配置没有写明 HMAC 的哈希算法，实施时按 coturn 源码或 `turnutils_uclient` 核对（TURN REST API 草案使用 HMAC-SHA1）。
- **共享密钥**：access 与 coturn 共用一个密钥，放在被忽略的 `.env` 中，不进仓库。
- **防滥用**：TURN 能把流量转发到任意地址，必须禁止转发到内网和本机地址（`denied-peer-ip`），否则可以被用来访问服务器所在网络；限制每个用户和全局的分配数（`user-quota`、`total-quota`）。
- **端口约定**：容器宿主机端口为默认端口 + 10000，只绑定 `127.0.0.1`（AGENTS.md）。TURN 的中继端口要能被浏览器直接访问，本地开发与公网部署的做法不同，见第 6 节。
- **资源**：新增一个 coturn 容器；不新增 Go 依赖（HMAC 与 Base64 用标准库）。

## 3. 方案

### 3.1 coturn 容器

Compose 新增 `coturn` 服务，放在单独的 profile（例如 `call`），默认不启动，与监控组件的做法一致。主要配置（`deploy/coturn/turnserver.conf`）：

```conf
listening-port=3478
# 本地开发只开一小段中继端口；公网部署时按并发通话数放大
min-port=49160
max-port=49200
fingerprint
use-auth-secret
# 由启动参数或环境变量传入，不写进仓库
# static-auth-secret=...
realm=marschat.local
no-multicast-peers
# 禁止中继到内网与本机地址
denied-peer-ip=0.0.0.0-0.255.255.255
denied-peer-ip=10.0.0.0-10.255.255.255
denied-peer-ip=127.0.0.0-127.255.255.255
denied-peer-ip=169.254.0.0-169.254.255.255
denied-peer-ip=172.16.0.0-172.31.255.255
denied-peer-ip=192.168.0.0-192.168.255.255
denied-peer-ip=::1
denied-peer-ip=fc00::-fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff
denied-peer-ip=fe80::-febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff
user-quota=4
total-quota=200
```

镜像版本在实施时固定到具体标签，并登记到 deployment.md。

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
  string credential = 3;     // base64(HMAC(secret, username))
}
```

时机正好：主叫在收到 `CallAccepted` 后、被叫在收到接听的 `CallAck` 后才创建 `RTCPeerConnection`（前端设计 3.9.5），这时两边都已拿到凭据，不需要额外的请求。

access 配置新增：

```yaml
call:
  turn:
    # 为空时不下发 ICE 服务器（现在的行为）
    urls: ${TURN_URLS:}
    secret: ${MARSCHAT_TURN_SECRET:}
    # 凭据有效期。浏览器续期中继分配时要重新认证，凭据过期后续期失败、中继断开，所以要覆盖最长的通话（实施时验证）
    ttl: 12h
```

### 3.3 前端

- 引擎的 `iceServers` 依赖改为读取这通通话的 `CallAck` 里的列表，不再固定为空数组；
- 开发时可以用 `iceTransportPolicy: "relay"` 强制只走中继，验证 coturn 与凭据（只在开发环境通过开关启用）；
- 文案不变：TURN 也连不通时仍按「网络无法直连」结束。

## 4. 接口、配置与数据变化

- `api/access/v1/call.proto`：`CallAck` 新增 `ice_servers`，新增 `IceServer`。只加字段，`buf breaking` 应当通过；`data-flow.md` 第 9 节同步。
- access：读取 `call.turn` 配置，生成凭据；`deployment.md` 补配置项。
- Compose：新增 `coturn` 服务与 profile，`deployment.md` 补端口与启动方式。
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
| 本地开发中继端口只绑定 `127.0.0.1`，coturn 在容器里看到的是容器地址 | 中继端口按相同端口号映射，并用 `external-ip=127.0.0.1/<容器地址>` 告诉 coturn 对外地址；实施时用 relay-only 验证，不通再调整 |
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

## 参考

- coturn 示例配置中的选项说明与 TURN REST API 约定：<https://github.com/coturn/coturn/blob/master/examples/etc/turnserver.conf>
