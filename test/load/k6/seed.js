// 准备压测数据，结果写入 /load/users.json（宿主机的 tmp/load/users.json）。
//
//   MODE=seed（默认）：经网关注册 USERS 个用户；第 2k 与 2k+1 个用户互为好友（WebSocket 收发场景两两配对）；
//     第 0 个用户建一个热点群，初始成员为第 1～HOT_MEMBERS 个用户（热点读场景）。
//   MODE=refresh：用刷新令牌为已准备的用户换新的访问令牌。访问令牌只有 15 分钟有效，每轮压测前执行一次。
//
// 注册依赖 account 的 log 发送方和固定验证码，见 Makefile 的 load-seed。
// k6 不能直接写文件，所以全部工作在 setup() 中完成，再由 handleSummary() 把 setup 的返回值写入文件。
import { BASE, FIXED_CODE, PASSWORD, USERS_FILE, batch, chunks, clientIP } from './lib.js';

const MODE = __ENV.MODE || 'seed';
const USERS = parseInt(__ENV.USERS || '1000', 10);
const HOT_MEMBERS = parseInt(__ENV.HOT_MEMBERS || '30', 10);
// 每批并发的请求数；注册时 account 要计算 bcrypt，批太大只会让请求排队
const BATCH = parseInt(__ENV.BATCH || '40', 10);

// refresh 模式读取上一次的结果；seed 模式文件可能还不存在，不能在初始化阶段读取
const previous = MODE === 'refresh' ? JSON.parse(open(USERS_FILE)) : null;

export const options = {
  vus: 1,
  iterations: 1,
  setupTimeout: '20m',
};

export function setup() {
  if (MODE === 'refresh') {
    return refresh(previous);
  }
  if (MODE !== 'seed') {
    throw new Error(`unknown MODE ${MODE}`);
  }
  return seed();
}

function seed() {
  if (USERS % 2 !== 0 || HOT_MEMBERS >= USERS || HOT_MEMBERS > 50) {
    throw new Error('USERS must be even, HOT_MEMBERS < USERS and <= 50 (max initial members)');
  }
  // 邮箱和设备 ID 带本次运行的标识，重复准备数据不会冲突
  const run = Date.now().toString(36);
  const users = [];
  const started = Date.now();

  for (const c of chunks(USERS, BATCH)) {
    const email = (i) => `load-${run}-${i}@example.test`;
    batch('send_code', c.map((i) => ['POST', '/v1/auth/email-codes',
      { email: email(i), purpose: 'CODE_PURPOSE_REGISTER' }, clientIP(i)]));
    const res = batch('register', c.map((i) => ['POST', '/v1/auth/register', {
      email: email(i), code: FIXED_CODE, password: PASSWORD, nickname: `load-${i}`,
      device: { deviceId: `load-${run}-${i}`, platform: 'PLATFORM_WEB', deviceName: 'k6' },
    }, clientIP(i)]));
    res.forEach((r, k) => {
      users[c[k]] = { id: r.user.id, token: r.token.accessToken, refresh: r.token.refreshToken };
    });
  }
  console.log(`registered ${USERS} users in ${Date.now() - started}ms`);

  // 两两加好友：2k 向 2k+1 发申请，2k+1 接受
  for (const c of chunks(USERS / 2, BATCH)) {
    const sent = batch('friend_request', c.map((k) => ['POST', '/v1/friend-requests',
      { userId: users[2 * k + 1].id }, clientIP(2 * k), users[2 * k].token]));
    batch('friend_accept', c.map((k, n) => ['POST', `/v1/friend-requests/${sent[n].request.id}/accept`,
      {}, clientIP(2 * k + 1), users[2 * k + 1].token]));
  }

  // 热点群：群初始成员必须是群主的好友。第 1 个用户已经是第 0 个用户的好友，
  // 其余成员向第 0 个用户发申请（每人一次，不触发每人每小时 20 次的申请上限），由第 0 个用户接受
  const others = [];
  for (let i = 2; i <= HOT_MEMBERS; i++) {
    others.push(i);
  }
  if (others.length > 0) {
    const sent = batch('friend_request', others.map((i) => ['POST', '/v1/friend-requests',
      { userId: users[0].id }, clientIP(i), users[i].token]));
    batch('friend_accept', others.map((i, n) => ['POST', `/v1/friend-requests/${sent[n].request.id}/accept`,
      {}, clientIP(0), users[0].token]));
  }
  const memberIds = [];
  for (let i = 1; i <= HOT_MEMBERS; i++) {
    memberIds.push(users[i].id);
  }
  const [group] = batch('create_group', [['POST', '/v1/groups',
    { name: `load-hot-${run}`, joinMode: 'JOIN_MODE_APPROVAL', memberIds }, clientIP(0), users[0].token]]);
  console.log(`seeded in ${Date.now() - started}ms, hot group ${group.group.id}`);

  return {
    run,
    createdAt: new Date().toISOString(),
    hotUserId: users[0].id,
    hotGroupId: group.group.id,
    // 热点群成员是第 0～hotMembers 个用户
    hotMembers: HOT_MEMBERS,
    users,
  };
}

function refresh(data) {
  const started = Date.now();
  for (const c of chunks(data.users.length, BATCH)) {
    const res = batch('refresh', c.map((i) => ['POST', '/v1/auth/refresh',
      { refreshToken: data.users[i].refresh }, clientIP(i)]));
    res.forEach((r, k) => {
      data.users[c[k]].token = r.token.accessToken;
      data.users[c[k]].refresh = r.token.refreshToken;
    });
  }
  console.log(`refreshed ${data.users.length} tokens in ${Date.now() - started}ms`);
  data.refreshedAt = new Date().toISOString();
  return data;
}

export default function () {}

export function handleSummary(summary) {
  const data = summary.setup_data;
  if (!data || !data.users) {
    // setup 失败时不覆盖已有的文件
    return { stdout: `no data written, see errors above (target ${BASE})\n` };
  }
  return {
    [USERS_FILE]: JSON.stringify(data),
    stdout: `${MODE}: ${data.users.length} users, hot user ${data.hotUserId}, hot group ${data.hotGroupId} -> tmp/load/users.json\n`,
  };
}
