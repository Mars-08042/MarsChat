package biz

import (
	"context"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/pkg/telemetry"
)

// access 的指标：
//   - marschat_ws_connections：本实例已认证的连接数，Grafana 中按实例求和得到在线连接总数；
//   - marschat_ws_closes_total{code}：连接结束的原因。服务端主动关闭时为关闭码（4001 未认证、4002 被踢、
//     4003 发送队列满、4008 帧频率超限、1001 停机），对端关闭为 peer_normal（1000、1001）或 peer_other，
//     其余为 heartbeat_timeout、too_large、read_error。对端的关闭码不直接作为标签，避免基数失控；
//   - marschat_ws_frames_total{type}：上行帧数，按帧类型；
//   - marschat_call_events_total{event, reason}：通话的发起、接听与结束，结束时 reason 为结束原因。
var (
	wsConnections = telemetry.Int64UpDownCounter("marschat_ws_connections", "本实例已认证的 WebSocket 连接数")
	wsCloses      = telemetry.Int64Counter("marschat_ws_closes", "WebSocket 连接结束次数，按原因区分")
	wsFrames      = telemetry.Int64Counter("marschat_ws_frames", "上行 WebSocket 帧数，按类型区分")
	callEvents    = telemetry.Int64Counter("marschat_call_events", "通话事件数")
)

// 对端关闭与读错误的分类，作为 marschat_ws_closes_total 的 code 标签
const (
	closePeerNormal       = "peer_normal"
	closePeerOther        = "peer_other"
	closeTooLarge         = "too_large"
	closeHeartbeatTimeout = "heartbeat_timeout"
	closeReadError        = "read_error"
)

func countClose(code string) {
	wsCloses.Add(context.Background(), 1, metric.WithAttributes(attribute.String("code", code)))
}

func countFrame(typ string) {
	wsFrames.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", typ)))
}

func countCallEvent(event string, reason v1.CallEndReason) {
	r := ""
	if reason != v1.CallEndReason_CALL_END_REASON_UNSPECIFIED {
		r = strings.ToLower(strings.TrimPrefix(reason.String(), "CALL_END_REASON_"))
	}
	callEvents.Add(context.Background(), 1, metric.WithAttributes(attribute.String("event", event), attribute.String("reason", r)))
}

// peerCloseLabel 把对端发来的关闭码归类：正常关闭和离开页面为 peer_normal，其余为 peer_other。
func peerCloseLabel(code int) string {
	if code == 1000 || code == 1001 {
		return closePeerNormal
	}
	return closePeerOther
}

// serverCloseLabel 是服务端主动关闭时的标签，关闭码由服务端选定，取值有限。
func serverCloseLabel(code int) string {
	return strconv.Itoa(code)
}

// startFrameSpan 为一个业务帧开服务端 Span。WebSocket 不经过 Kratos 中间件，这里是这条请求链路的起点，
// 后续对 message、social 的 gRPC 调用经客户端中间件接在它下面。认证帧和心跳不开 Span。
func (c *Conn) startFrameSpan(name string, rid uint64) (context.Context, trace.Span) {
	return otel.Tracer(telemetry.ScopeName).Start(context.Background(), name,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.Int64("marschat.user_id", c.principal.UserID),
			attribute.Int64("marschat.conn_id", c.id.Load()),
			attribute.Int64("marschat.request_id", int64(rid)),
		))
}
