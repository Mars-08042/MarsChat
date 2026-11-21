package biz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"

	accessv1 "github.com/Mars-08042/MarsChat/api/access/v1"
	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	messagev1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/pkg/route"
	"github.com/Mars-08042/MarsChat/pkg/telemetry/telemetrytest"
)

// errPusher 按地址返回预设的错误，没有预设的地址推送成功。
type errPusher struct{ errs map[string]error }

func (p errPusher) Push(_ context.Context, addr string, ids []int64, _ *accessv1.ServerFrame) (int, error) {
	if err := p.errs[addr]; err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (p errPusher) Kick(_ context.Context, addr string, ids []int64, _ accountv1.RevokeReason) (int, error) {
	return p.Push(context.Background(), addr, ids, nil)
}

func pushes(t *testing.T, kind, result string) int64 {
	return telemetrytest.Sum(t, "marschat_push", attribute.String("kind", kind), attribute.String("result", result))
}

// 每个实例调用按结果计数，成功的调用记一次投递延迟；接收方都不在线时只计一次 offline。
// 计数器在包内的测试之间累计，所以比较差值。
func TestPushMetrics(t *testing.T) {
	telemetrytest.Reader(t)
	routes := &fakeRoutes{m: map[int64][]route.Entry{
		1: {{ConnID: 11, Addr: "ok"}},
		2: {{ConnID: 21, Addr: "down"}},
		3: {{ConnID: 31, Addr: "cooling"}},
	}}
	pusher := errPusher{errs: map[string]error{
		"down":    errors.New("unavailable"),
		"cooling": ErrInstanceCoolingDown,
	}}
	uc := NewDeliveryUsecase(routes, &fakeDedupe{seen: map[string]bool{}}, &fakeMembers{}, pusher,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	before := map[string]int64{}
	for _, r := range []string{"ok", "failed", "cooling", "offline"} {
		before[r] = pushes(t, kindMessage, r)
	}
	single := messagev1.ConversationType_CONVERSATION_TYPE_SINGLE
	latencyBefore := telemetrytest.HistogramCount(t, "marschat_message_delivery_seconds", attribute.String("conversation_type", single.String()))

	created := timestamppb.New(time.Now().Add(-50 * time.Millisecond))
	uc.OnMessageCreated(context.Background(), "metrics:1", &messagev1.MessageCreated{
		Message:   &messagev1.Message{ConversationId: 1, ConversationType: single, Seq: 1, CreatedAt: created},
		MemberIds: []int64{1, 2, 3},
	})
	uc.OnMessageCreated(context.Background(), "metrics:2", &messagev1.MessageCreated{
		Message:   &messagev1.Message{ConversationId: 2, ConversationType: single, Seq: 1, CreatedAt: created},
		MemberIds: []int64{4, 5},
	})

	for result, want := range map[string]int64{"ok": 1, "failed": 1, "cooling": 1, "offline": 1} {
		if got := pushes(t, kindMessage, result) - before[result]; got != want {
			t.Errorf("push{kind=message,result=%s} += %d, want %d", result, got, want)
		}
	}
	if got := telemetrytest.HistogramCount(t, "marschat_message_delivery_seconds", attribute.String("conversation_type", single.String())) - latencyBefore; got != 1 {
		t.Errorf("delivery latency samples += %d, want 1 (only the successful instance call)", got)
	}
}
