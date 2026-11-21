// 热点读压测：以固定到达率读少量热点资源，调用方是热点群的成员。
//
//   MODE=hot（默认）：轮流读热点用户资料（account 缓存）、热点群资料与成员（social 缓存）、会话列表（message）。
//     击穿实验在运行中执行 make load-bust，删除热点群的两个缓存键，观察回源次数与延迟。
//   MODE=penetration：用不存在的 ID 读用户资料和群资料，每个 ID 只用一次，空值缓存挡不住，
//     靠布隆过滤器拦截；预期返回 404，MySQL 基本收不到查询。
//   RATE（默认 300）：每秒请求数；DURATION（默认 60 秒）。
import exec from 'k6/execution';
import http from 'k6/http';
import { check } from 'k6';
import { SharedArray } from 'k6/data';
import { BASE, TREND_STATS, USERS_FILE, clientIP, params } from './lib.js';

const MODE = __ENV.MODE || 'hot';
const RATE = parseInt(__ENV.RATE || '300', 10);
const DURATION = parseInt(__ENV.DURATION || '60', 10);

// 只取热点群成员（第 0～hotMembers 个用户）和热点 ID，避免每个 VU 各自解析整个文件
const data = new SharedArray('hot', () => {
  const d = JSON.parse(open(USERS_FILE));
  return [{
    hotUserId: d.hotUserId,
    hotGroupId: d.hotGroupId,
    members: d.users.slice(0, d.hotMembers + 1).map((u) => u.token),
  }];
})[0];

const endpoints = MODE === 'penetration'
  ? ['missing_user', 'missing_group']
  : ['get_user', 'get_group', 'group_members', 'conversations'];

const thresholds = { http_req_failed: ['rate<0.01'] };
for (const name of endpoints) {
  // 每个接口一个阈值，k6 才会在摘要中单独列出它的延迟
  thresholds[`http_req_duration{name:${name}}`] = ['p(99)<200'];
}

export const options = {
  scenarios: {
    read: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: `${DURATION}s`,
      preAllocatedVUs: 50,
      maxVUs: 300,
    },
  },
  summaryTrendStats: TREND_STATS,
  thresholds,
};

// 穿透场景中 404 是预期结果，不计入 http_req_failed
http.setResponseCallback(http.expectedStatuses(200, 404));

export default function () {
  const n = exec.scenario.iterationInTest;
  const caller = n % data.members.length;
  const p = (name) => params(clientIP(caller), data.members[caller], name);
  const name = endpoints[n % endpoints.length];
  let res;
  switch (name) {
    case 'get_user':
      res = http.get(`${BASE}/v1/users/${data.hotUserId}`, p(name));
      break;
    case 'get_group':
      res = http.get(`${BASE}/v1/groups/${data.hotGroupId}`, p(name));
      break;
    case 'group_members':
      res = http.get(`${BASE}/v1/groups/${data.hotGroupId}/members`, p(name));
      break;
    case 'conversations':
      res = http.get(`${BASE}/v1/conversations`, p(name));
      break;
    case 'missing_user':
      res = http.get(`${BASE}/v1/users/${missingID(n)}`, p(name));
      break;
    case 'missing_group':
      res = http.get(`${BASE}/v1/groups/${missingID(n)}`, p(name));
      break;
  }
  const want = MODE === 'penetration' ? 404 : 200;
  check(res, { [`status ${want}`]: (r) => r.status === want });
}

// missingID 返回不存在的 ID：19 位、以 8 开头，大于目前发号器生成的 ID，仍在 int64 范围内。
// 用字符串拼接，JS 的 number 表示不了这么大的整数。
function missingID(n) {
  return `8${String(n).padStart(18, '0')}`;
}
