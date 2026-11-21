// WebSocket 收发压测：第 j 个连接使用第 j 个用户，向好友（第 j^1 个用户）每秒发 1 条单聊消息。
//
//   CONNS（默认 200）：连接总数，不超过准备的用户数；CONNS_PER_VU（默认 10）：每个 VU 持有的连接数，
//   一个 VU 是一个 JS 运行时，多个连接共用可以减少 k6 自身的内存；RAMP（默认 10 秒）：所有连接在这段时间内
//   陆续建立；DURATION（默认 60 秒）：全部连接建立后持续发送的时长。
//
// 统计（只统计全部连接建立之后的稳态窗口）：
//   ws_ack_ms：发出 SendMessage 到收到 SendAck，即 access → message 写库的往返；
//   ws_push_ms：接收方收到推送的时刻减去正文中的发送时刻，即发送 → 写库 → Outbox → Kafka → delivery → access 全程。
//   收发双方在同一个 k6 进程中，用同一个时钟。
// 帧使用 JSON 子协议 marschat.v1.json（k6/websockets 不支持 protocols 参数，直接设置握手头）。
import exec from 'k6/execution';
import { SharedArray } from 'k6/data';
import { Counter, Trend } from 'k6/metrics';
import { setInterval, setTimeout, clearInterval } from 'k6/timers';
import { WebSocket } from 'k6/websockets';
import { TREND_STATS, USERS_FILE, WS_URL, clientIP } from './lib.js';

const CONNS = parseInt(__ENV.CONNS || '200', 10);
const PER_VU = parseInt(__ENV.CONNS_PER_VU || '10', 10);
const RAMP = parseInt(__ENV.RAMP || '10', 10);
const DURATION = parseInt(__ENV.DURATION || '60', 10);
const VUS = Math.ceil(CONNS / PER_VU);
// 停止发送后再等几秒收完在途的推送，然后关闭连接
const DRAIN_MS = 3000;

const users = new SharedArray('users', () => JSON.parse(open(USERS_FILE)).users);

const ackMs = new Trend('ws_ack_ms', true);
const pushMs = new Trend('ws_push_ms', true);
const authOK = new Counter('ws_auth_ok');
const sent = new Counter('ws_sent');
const acked = new Counter('ws_acked');
const pushed = new Counter('ws_pushed');
const errorFrames = new Counter('ws_error_frames');
const unexpectedClose = new Counter('ws_unexpected_close');

export const options = {
  scenarios: {
    chat: {
      executor: 'per-vu-iterations',
      vus: VUS,
      iterations: 1,
      maxDuration: `${RAMP + DURATION + 60}s`,
    },
  },
  summaryTrendStats: TREND_STATS,
  thresholds: {
    ws_auth_ok: [`count==${CONNS}`],
    ws_unexpected_close: ['count==0'],
    ws_error_frames: ['count==0'],
    ws_ack_ms: ['p(99)<500'],
    ws_push_ms: ['p(99)<1000'],
  },
};

export function setup() {
  if (CONNS > users.length || CONNS % 2 !== 0) {
    throw new Error(`CONNS must be even and <= ${users.length} (prepared users)`);
  }
}

export default function () {
  const start = exec.scenario.startTime;
  const steadyAt = start + (RAMP + 5) * 1000;
  const stopAt = start + (RAMP + DURATION) * 1000;
  // 本轮运行的标识，拼进 client_msg_id：同一发送方的 client_msg_id 重复时服务端按幂等只存一条
  const runTag = start.toString(36);
  const vu = exec.vu.idInTest - 1;
  setTimeout(() => {
    for (let k = 0; k < PER_VU; k++) {
      const j = vu * PER_VU + k;
      if (j < CONNS) {
        connect(j, { steadyAt, stopAt, runTag });
      }
    }
  }, (vu * RAMP * 1000) / VUS);
}

function connect(j, t) {
  const me = users[j];
  const peer = users[j ^ 1];
  const ws = new WebSocket(WS_URL, null, {
    headers: { 'Sec-WebSocket-Protocol': 'marschat.v1.json', 'X-Forwarded-For': clientIP(j) },
  });
  const pending = new Map();
  let rid = 0;
  let timer = null;
  let closing = false;

  const send = () => {
    if (ws.readyState !== WebSocket.OPEN) {
      clearInterval(timer);
      return;
    }
    const now = Date.now();
    if (now >= t.stopAt) {
      clearInterval(timer);
      setTimeout(() => {
        closing = true;
        ws.close();
      }, DRAIN_MS);
      return;
    }
    const id = String(++rid);
    pending.set(id, now);
    ws.send(JSON.stringify({
      requestId: id,
      sendMessage: {
        peerType: 'CONVERSATION_TYPE_SINGLE',
        peerId: peer.id,
        clientMsgId: `${t.runTag}-${id}`,
        content: { text: { text: `t=${now}` } },
      },
    }));
    sent.add(1);
  };

  ws.onopen = () => {
    ws.send(JSON.stringify({ requestId: String(++rid), auth: { accessToken: me.token } }));
  };

  ws.onmessage = (e) => {
    const f = JSON.parse(e.data);
    const now = Date.now();
    if (f.sendAck) {
      const sentAt = pending.get(f.requestId);
      pending.delete(f.requestId);
      if (sentAt !== undefined) {
        acked.add(1);
        if (sentAt >= t.steadyAt) {
          ackMs.add(now - sentAt);
        }
      }
    } else if (f.message) {
      const m = /^t=(\d+)$/.exec(f.message.content.text ? f.message.content.text.text : '');
      if (m) {
        const sentAt = parseInt(m[1], 10);
        pushed.add(1);
        if (sentAt >= t.steadyAt) {
          pushMs.add(now - sentAt);
        }
      }
    } else if (f.authOk) {
      authOK.add(1);
      // 发送时刻在一秒内随机错开，避免所有连接同时发送
      setTimeout(() => {
        send();
        timer = setInterval(send, 1000);
      }, Math.random() * 1000);
    } else if (f.error) {
      pending.delete(f.requestId);
      errorFrames.add(1, { reason: f.error.reason });
    }
  };

  ws.onclose = (e) => {
    clearInterval(timer);
    if (!closing) {
      unexpectedClose.add(1, { code: String(e.code) });
    }
  };

  ws.onerror = (e) => {
    console.warn(`conn ${j}: ${e.error}`);
  };
}
