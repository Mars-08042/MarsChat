package biz

import (
	"github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
)

func newErr(code int, reason v1.ErrorReason, message string) *errors.Error {
	return errors.New(code, reason.String(), message)
}

// ErrInvalidFrame 表示帧无法解码、没有内容或发送时机不对。
func ErrInvalidFrame(message string) error {
	return newErr(400, v1.ErrorReason_INVALID_FRAME, message)
}

// ErrUpstreamUnavailable 表示依赖（message、social、Redis）调用失败或超时。发消息时消息可能已经写入，
// 客户端用同一个 client_msg_id 重发即可。cause 只进日志，不返回给客户端。
func ErrUpstreamUnavailable(cause error) error {
	return newErr(503, v1.ErrorReason_UPSTREAM_UNAVAILABLE, "服务暂时不可用，请稍后重试").WithCause(cause)
}

// ErrCallNotFound 表示通话不存在或已结束、当前连接不是通话方，或通话状态不允许该操作。
func ErrCallNotFound() error {
	return newErr(404, v1.ErrorReason_CALL_NOT_FOUND, "通话不存在或已结束")
}

// ErrCallPeerOffline 表示呼叫时被叫没有在线连接。
func ErrCallPeerOffline() error {
	return newErr(404, v1.ErrorReason_CALL_PEER_OFFLINE, "对方不在线")
}

// ErrCallBusy 表示被叫正在通话或振铃中。
func ErrCallBusy() error {
	return newErr(409, v1.ErrorReason_CALL_BUSY, "对方正在通话中")
}

// ErrAlreadyInCall 表示自己（包括其他设备）已在通话或振铃中。
func ErrAlreadyInCall() error {
	return newErr(409, v1.ErrorReason_ALREADY_IN_CALL, "你正在通话中")
}

// ErrCallAlreadyAnswered 表示来电已被被叫的另一台设备接听。
func ErrCallAlreadyAnswered() error {
	return newErr(409, v1.ErrorReason_CALL_ALREADY_ANSWERED, "已在其他设备接听")
}

// errInternal 与 accesslog 中间件的内部错误一致，不把内部细节返回给客户端。
var errInternal = errors.InternalServer("INTERNAL", "服务器内部错误")

// errorFrame 把错误转成 Error 帧。没有错误原因的错误（非 Kratos 错误）按内部错误返回。
func errorFrame(requestID uint64, err error) *v1.ServerFrame {
	e := errors.FromError(err)
	if e.Reason == "" {
		e = errInternal
	}
	return &v1.ServerFrame{RequestId: requestID, Body: &v1.ServerFrame_Error{Error: &v1.Error{
		Code: e.Code, Reason: e.Reason, Message: e.Message, Metadata: e.Metadata,
	}}}
}
