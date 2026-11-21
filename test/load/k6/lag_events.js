// 积压告警实验：先停止 message 的全部实例，再运行本脚本。每个用户向另外两个用户发好友申请，
// 产生 2 × 用户数条 social 事件；message 的消费组 marschat-message 不再消费 marschat.social.events，
// 积压超过 1000 条持续 5 分钟后 KafkaConsumerLag 告警触发，启动 message 后积压消化、告警结束。
// 积压由 delivery 用 kadm 统计，所以不能用停止 delivery 的方式实验：delivery 全部停止时积压指标也随之消失。
//
// 申请对象是第 i+100、i+300 个用户（取模），与准备数据时的好友关系和热点群都不重叠，也不会互相申请。
// 每人发 2 次，不触发每人每小时 20 次的申请上限。
import { batch, chunks, clientIP, USERS_FILE } from './lib.js';

const data = JSON.parse(open(USERS_FILE));
const OFFSETS = [100, 300];

export const options = { vus: 1, iterations: 1 };

export default function () {
  const n = data.users.length;
  if (n <= 2 * OFFSETS[OFFSETS.length - 1]) {
    throw new Error(`need more than ${2 * OFFSETS[OFFSETS.length - 1]} prepared users`);
  }
  for (const off of OFFSETS) {
    for (const c of chunks(n, 40)) {
      batch('friend_request', c.map((i) => ['POST', '/v1/friend-requests',
        { userId: data.users[(i + off) % n].id }, clientIP(i), data.users[i].token]));
    }
  }
  console.log(`sent ${OFFSETS.length * n} friend requests`);
}
