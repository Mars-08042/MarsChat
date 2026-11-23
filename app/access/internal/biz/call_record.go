package biz

import (
	"context"
	"log/slog"
	"math"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
)

// 通话记录（docs/design/call-record.md）：每通通话结束时在两人的单聊会话里写一条，发送者为主叫。
// 写入在后台进行，不阻塞信令；message 不可用时按 defaultRecordRetry 重试，仍然失败就放弃并记 WARN，
// 通话本身不受影响。access 没有数据库，进程在写入之前退出时这条记录会丢失。

var defaultRecordRetry = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// 单次写入的时限，message 客户端另有自己的超时
const recordWriteTimeout = 5 * time.Second

// callRecord 把结束原因映射为通话记录，不需要记录时返回 nil。endedAt 减去接听时间就是通话时长。
func callRecord(call *Call, reason v1.CallEndReason, endedAt time.Time) *messagev1.CallContent {
	media := messagev1.CallRecordMedia_CALL_RECORD_MEDIA_AUDIO
	if call.Media == v1.CallMedia_CALL_MEDIA_VIDEO {
		media = messagev1.CallRecordMedia_CALL_RECORD_MEDIA_VIDEO
	}
	c := &messagev1.CallContent{Media: media}
	answered := !call.AnsweredAt.IsZero()
	switch reason {
	case v1.CallEndReason_CALL_END_REASON_HANGUP, v1.CallEndReason_CALL_END_REASON_DISCONNECTED:
		switch {
		case answered:
			c.Result = messagev1.CallResult_CALL_RESULT_COMPLETED
			c.DurationSeconds = durationSeconds(endedAt.Sub(call.AnsweredAt))
		case reason == v1.CallEndReason_CALL_END_REASON_HANGUP:
			// 振铃中只有主叫能挂断（被叫是拒绝）
			c.Result = messagev1.CallResult_CALL_RESULT_CANCELLED
		default:
			c.Result = messagev1.CallResult_CALL_RESULT_FAILED
		}
	case v1.CallEndReason_CALL_END_REASON_REJECTED:
		c.Result = messagev1.CallResult_CALL_RESULT_REJECTED
	case v1.CallEndReason_CALL_END_REASON_TIMEOUT:
		c.Result = messagev1.CallResult_CALL_RESULT_MISSED
	case v1.CallEndReason_CALL_END_REASON_PEER_OFFLINE:
		c.Result = messagev1.CallResult_CALL_RESULT_FAILED
	default:
		// ANSWERED_ELSEWHERE 只是被叫其他设备上的结束，不是整通通话的结束，不会走到这里；其余原因不认识
		return nil
	}
	return c
}

// durationSeconds 四舍五入到秒，超出 int32 时截断（通话受心跳与 TTL 约束，实际不会出现）。
func durationSeconds(d time.Duration) int32 {
	s := math.Round(max(d, 0).Seconds())
	return int32(min(s, math.MaxInt32))
}

// recordEnded 在后台写通话记录。停机之后结束的通话不再写入（stop 已经在等待进行中的写入）。
func (k *Calls) recordEnded(call *Call, reason v1.CallEndReason, endedAt time.Time) {
	c := callRecord(call, reason, endedAt)
	if c == nil || k.records == nil {
		return
	}
	k.mu.Lock()
	if k.stopped {
		k.mu.Unlock()
		k.logger.Warn("call record dropped: shutting down", slog.Int64("call_id", call.ID))
		countCallRecord("dropped")
		return
	}
	k.recordWG.Add(1)
	k.mu.Unlock()
	go k.writeRecord(call, c)
}

// writeRecord 写入并按 recordRetry 重试。WriteCallRecord 按通话 ID 幂等，超时后重试不会写出两条。
// 带原因的 4xx 错误（参数不合法）重试也不会成功，直接放弃；停机时不再等待下一次重试。
func (k *Calls) writeRecord(call *Call, c *messagev1.CallContent) {
	defer k.recordWG.Done()
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), recordWriteTimeout)
		err := k.records.WriteCallRecord(ctx, call.ID, call.CallerID, call.CalleeID, c)
		cancel()
		if err == nil {
			countCallRecord("written")
			return
		}
		attrs := []any{slog.Int64("call_id", call.ID), slog.Int("attempt", attempt+1), slog.Any("error", err)}
		if kerrors.FromError(err).Code < 500 || attempt >= len(k.recordRetry) {
			k.logger.Warn("write call record failed, giving up", attrs...)
			countCallRecord("failed")
			return
		}
		select {
		case <-time.After(k.recordRetry[attempt]):
		case <-k.recordStop:
			k.logger.Warn("write call record failed, not retrying during shutdown", attrs...)
			countCallRecord("dropped")
			return
		}
	}
}
