// 压测脚本的公共部分：网关地址、虚拟客户端地址和 JSON 请求。
// 脚本在 Compose 网络中运行（docker compose run --rm k6），经网关访问各服务。
import http from 'k6/http';

export const BASE = __ENV.BASE_URL || 'http://apisix:9080';
export const WS_URL = __ENV.WS_URL || 'ws://apisix:9080/ws';
// 准备数据的输出，也是其他脚本的输入；挂载自宿主机的 tmp/load
export const USERS_FILE = '/load/users.json';
export const PASSWORD = 'abc12345';
// account 的 log 发送方使用的固定验证码，压测前要把 account 覆盖为 log 模式
export const FIXED_CODE = '000000';

export const TREND_STATS = ['avg', 'min', 'med', 'p(90)', 'p(99)', 'max', 'count'];

// clientIP 返回第 n 个虚拟客户端的来源地址，取自 RFC 2544 的基准测试网段 198.18.0.0/15，
// 不会和真实地址混淆。网关只对 k6 容器信任 X-Forwarded-For（见 deploy/apisix/apisix.yaml），
// 每个虚拟客户端用不同的地址，网关和 account 的按 IP 限流按虚拟客户端计算。
export function clientIP(n) {
  return `198.${18 + ((n >> 16) & 1)}.${(n >> 8) & 255}.${n & 255}`;
}

// params 构造请求参数。name 作为指标标签，用于按接口统计延迟。
export function params(ip, token, name) {
  const headers = { 'Content-Type': 'application/json', 'X-Forwarded-For': ip };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const p = { headers };
  if (name) {
    p.tags = { name };
  }
  return p;
}

// batch 并发发送一组 JSON 请求，任何一个失败都抛出异常并带上错误原因，返回解析后的响应体。
// 请求为 [method, path, body, ip, token]。
export function batch(what, reqs) {
  const res = http.batch(reqs.map(([method, path, body, ip, token]) => ({
    method,
    url: BASE + path,
    body: body === undefined ? null : JSON.stringify(body),
    params: params(ip, token, what),
  })));
  return res.map((r, i) => {
    if (r.status !== 200) {
      // 错误响应只含 Kratos 的错误原因，不含令牌
      throw new Error(`${what} #${i}: HTTP ${r.status} ${String(r.body).slice(0, 200)}`);
    }
    return r.json();
  });
}

// chunks 把 [0, n) 按 size 分段，用于分批并发。
export function chunks(n, size) {
  const out = [];
  for (let i = 0; i < n; i += size) {
    const c = [];
    for (let j = i; j < Math.min(n, i + size); j++) {
      c.push(j);
    }
    out.push(c);
  }
  return out;
}
