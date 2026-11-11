const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { test } = require("node:test");
const { parseSync } = require("@babel/core");

// 执行组件的实际通话函数，用浏览器替身覆盖环境和设备失败，不依赖摄像头或服务端。
const component = fs.readFileSync(
  path.join(__dirname, "../src/views/chat/contact/ContactChat.vue"), "utf8"
);
const script = component.match(/<script>([\s\S]*?)<\/script>/)[1];
const ast = parseSync(script, { configFile: false, babelrc: false });
const setup = ast.program.body.find(node => node.type === "ExportDefaultDeclaration")
  .declaration.properties.find(node => node.key.name === "setup");
const names = ["createRtcPeerConnection", "closeRtcPeerConnection", "getLocalMediaStream",
  "closeLocalMediaStream", "attachMediaStreamToLocalVideo", "attachMediaStreamToPeerConnection", "startCall"];
const functions = setup.body.body.filter(node => node.type === "VariableDeclaration"
  && names.includes(node.declarations[0].id.name));
assert.equal(functions.length, names.length);
const source = functions.map(node => script.slice(node.start, node.end)).join("\n");

function harness(options = {}) {
  const events = { errors: [], warnings: [], sent: [], reads: 0, closed: 0, stopped: 0, added: 0 };
  const track = { stop() { events.stopped++; } };
  const stream = { getTracks() { return [track]; } };
  const data = {
    localStream: null, localVideo: null, rtcPeerConn: null, ICE_CFG: {},
    isStartingCall: false, ableToStartCall: true, ableToReceiveOrRejectCall: true,
    sessionId: "S1", userInfo: { uuid: "U1", nickname: "test", avatar: "/static/avatars/a.png" },
    contactInfo: { contact_id: "U2" },
  };
  class Peer {
    close() { events.closed++; }
    addTrack() { events.added++; }
  }
  const context = {
    data,
    window: { isSecureContext: options.secure !== false, RTCPeerConnection: options.rtc === false ? undefined : Peer },
    navigator: { mediaDevices: options.media === false ? undefined : {
      getUserMedia() { events.reads++; return options.acquire ? options.acquire(stream) : Promise.resolve(stream); },
    } },
    document: { querySelector() { return { style: {}, srcObject: null }; } },
    store: { state: { socket: { send(value) {
      if (options.sendError) throw new Error("信令连接已断开");
      events.sent.push(JSON.parse(value));
    } } } },
    ElMessage: { error(value) { events.errors.push(value); }, warning(value) { events.warnings.push(value); } },
    console: { log() {}, error() {} },
  };
  vm.createContext(context);
  vm.runInContext(`${source}\nglobalThis.start = startCall;`, context);
  return { data, events, start: context.start };
}

test("局域网 HTTP 在访问设备和创建连接前退出，发起与接听均提示 HTTPS", async () => {
  for (const initiator of [true, false]) {
    const h = harness({ secure: false });
    await h.start(initiator);
    assert.match(h.events.errors[0], /HTTPS/);
    assert.equal(h.events.reads, 0);
    assert.equal(h.events.sent.length, 0);
    assert.equal(h.data.rtcPeerConn, null);
    assert.equal(h.data.isStartingCall, false);
  }
});

test("WebRTC 或媒体接口不可用时明确提示且不发送通话信令", async () => {
  for (const options of [{ rtc: false }, { media: false }]) {
    const h = harness(options);
    await h.start(true);
    assert.equal(h.events.errors.length, 1);
    assert.equal(h.events.sent.length, 0);
    assert.equal(h.data.rtcPeerConn, null);
    assert.equal(h.data.localStream, null);
  }
});

test("权限拒绝、设备缺失和设备占用均清理连接并允许重试", async () => {
  for (const name of ["NotAllowedError", "NotFoundError", "NotReadableError"]) {
    const h = harness({ acquire() { return Promise.reject(Object.assign(new Error("device"), { name })); } });
    await h.start(false);
    assert.equal(h.events.errors.length, 1);
    assert.equal(h.events.closed, 1);
    assert.equal(h.events.sent.length, 0);
    assert.equal(h.data.rtcPeerConn, null);
    assert.equal(h.data.localVideo, null);
    assert.equal(h.data.isStartingCall, false);
    assert.equal(h.data.ableToReceiveOrRejectCall, true);
  }
});

test("媒体接口异常返回 null 时不再调用 getTracks 或发送信令", async () => {
  const h = harness({ acquire() { return Promise.resolve(null); } });
  await h.start(true);
  assert.match(h.events.errors[0], /未获取到/);
  assert.equal(h.events.added, 0);
  assert.equal(h.events.closed, 1);
  assert.equal(h.events.sent.length, 0);
});

test("正常发起与接听保留原信令且绑定媒体轨道", async () => {
  for (const initiator of [true, false]) {
    const h = harness();
    await h.start(initiator);
    assert.equal(h.events.errors.length, 0);
    assert.equal(h.events.added, 1);
    assert.equal(h.events.sent.length, 1);
    const request = h.events.sent[0];
    assert.equal(request.type, 3);
    assert.equal(request.receive_id, "U2");
    assert.equal(JSON.parse(request.av_data).type, initiator ? "start_call" : "receive_call");
    assert.equal(h.data.isStartingCall, false);
  }
});

test("等待媒体授权期间重复点击不重复申请设备或发送通话", async () => {
  let resolve;
  const h = harness({ acquire(stream) { return new Promise(done => { resolve = () => done(stream); }); } });
  const pending = h.start(true);
  await h.start(true);
  assert.equal(h.events.reads, 1);
  assert.equal(h.events.warnings.length, 1);
  resolve();
  await pending;
  assert.equal(h.events.sent.length, 1);
});

test("信令发送失败会停止媒体轨道并清除本地预览，允许再次发起", async () => {
  const h = harness({ sendError: true });
  await h.start(true);
  assert.equal(h.events.errors.length, 1);
  assert.equal(h.events.stopped, 1);
  assert.equal(h.events.closed, 1);
  assert.equal(h.data.localVideo, null);
  assert.equal(h.data.localStream, null);
  assert.equal(h.data.isStartingCall, false);
});
