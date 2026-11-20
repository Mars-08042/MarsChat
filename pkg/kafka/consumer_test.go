package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/Mars-08042/MarsChat/pkg/conf"
)

// sink 记录写入死信队列和重试队列的记录，fail 非空时前 failN 次写入返回它。
type sink struct {
	mu    sync.Mutex
	recs  []*kgo.Record
	fail  error
	failN int
}

func (s *sink) write(_ context.Context, r *kgo.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil && s.failN > 0 {
		s.failN--
		return s.fail
	}
	s.recs = append(s.recs, r)
	return nil
}

func (s *sink) records() []*kgo.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*kgo.Record(nil), s.recs...)
}

func newTestConsumer() (*Consumer, *sink) {
	s := &sink{}
	return &Consumer{
		group:    "g",
		handlers: make(map[string]Handler),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:      time.Now,
		write:    s.write,
		retryMin: time.Millisecond,
		retryMax: 4 * time.Millisecond,
	}, s
}

func record(eventType string, payload []byte) *kgo.Record {
	return &kgo.Record{
		Topic: "t", Partition: 2, Offset: 41, Key: []byte("k"), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: HeaderEventType, Value: []byte(eventType)},
			{Key: HeaderEventID, Value: []byte("svc:1")},
			{Key: HeaderSource, Value: []byte("svc")},
		},
	}
}

// failing 返回前 n 次失败、之后成功的处理器和调用计数。
func failing(n int, err error) (Handler, *int) {
	calls := 0
	return func(context.Context, *Event) error {
		calls++
		if calls <= n {
			return err
		}
		return nil
	}, &calls
}

func TestProcessRetriesUntilSuccess(t *testing.T) {
	c, s := newTestConsumer()
	calls := 0
	c.Handle("x", func(_ context.Context, e *Event) error {
		calls++
		if e.ID != "svc:1" || e.Source != "svc" || e.Key != "k" {
			t.Errorf("unexpected event %+v", e)
		}
		if calls < 3 {
			return errors.New("db down")
		}
		return nil
	})
	if !c.process(context.Background(), record("x", nil)) {
		t.Fatal("process returned false")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if n := len(s.records()); n != 0 {
		t.Fatalf("%d records written, want none", n)
	}
}

// 没有配置重试队列（有序消费者）时，临时错误一直原地重试，不转走也不进死信队列。
func TestProcessWithoutRetryTopicNeverForwards(t *testing.T) {
	c, s := newTestConsumer()
	h, calls := failing(15, errors.New("db down"))
	c.Handle("x", h)
	if !c.process(context.Background(), record("x", nil)) {
		t.Fatal("process returned false")
	}
	if *calls != 16 {
		t.Fatalf("calls = %d, want 16", *calls)
	}
	if n := len(s.records()); n != 0 {
		t.Fatalf("%d records written, want none", n)
	}
}

// Permanent 错误、panic、解码失败都写入死信队列后跳过，不重试。
func TestPermanentFailuresGoToDeadLetter(t *testing.T) {
	c, s := newTestConsumer()
	calls := 0
	c.Handle("perm", func(context.Context, *Event) error { calls++; return Permanent(errors.New("bad content")) })
	c.Handle("panic", func(context.Context, *Event) error { calls++; panic("boom") })
	On(c, func(context.Context, *Event, *wrapperspb.StringValue) error { calls++; return nil })
	cases := []struct{ typ, errContains string }{
		{"perm", "bad content"},
		{"panic", "handler panic: boom"},
		{"google.protobuf.StringValue", "decode google.protobuf.StringValue"},
	}
	for _, tc := range cases {
		if !c.process(context.Background(), record(tc.typ, []byte{0xff})) {
			t.Fatalf("%s: process returned false", tc.typ)
		}
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2 (no retries, decode failure never reaches the handler)", calls)
	}
	recs := s.records()
	if len(recs) != len(cases) {
		t.Fatalf("dead letters = %d, want %d", len(recs), len(cases))
	}
	for i, r := range recs {
		tc := cases[i]
		if r.Topic != TopicDLQ || string(r.Key) != "k" || string(r.Value) != "\xff" {
			t.Errorf("%s: dead letter %s key=%q value=%q, want original key and value", tc.typ, r.Topic, r.Key, r.Value)
		}
		want := map[string]string{
			HeaderEventType: tc.typ, HeaderEventID: "svc:1", HeaderSource: "svc",
			HeaderDLQGroup: "g", HeaderDLQTopic: "t", HeaderDLQPartition: "2", HeaderDLQOffset: "41",
		}
		for k, v := range want {
			if got := Header(r, k); got != v {
				t.Errorf("%s: header %s = %q, want %q", tc.typ, k, got, v)
			}
		}
		if got := Header(r, HeaderDLQError); !strings.Contains(got, tc.errContains) {
			t.Errorf("%s: error header %q, want it to contain %q", tc.typ, got, tc.errContains)
		}
	}
}

// 死信队列写不进去时不提交原位点：原地重试写入直到成功，处理器不再执行。
func TestDeadLetterWriteRetriedUntilSuccess(t *testing.T) {
	c, s := newTestConsumer()
	s.fail, s.failN = errors.New("broker down"), 3
	h, calls := failing(100, Permanent(errors.New("bad")))
	c.Handle("x", h)
	if !c.process(context.Background(), record("x", nil)) {
		t.Fatal("process returned false")
	}
	if *calls != 1 {
		t.Fatalf("handler calls = %d, want 1", *calls)
	}
	if n := len(s.records()); n != 1 {
		t.Fatalf("dead letters = %d, want 1 after the retried write", n)
	}
}

func TestDeadLetterWriteAbortedOnShutdown(t *testing.T) {
	c, s := newTestConsumer()
	s.fail, s.failN = errors.New("broker down"), 1<<30
	ctx, cancel := context.WithCancel(context.Background())
	c.Handle("x", func(context.Context, *Event) error { return Permanent(errors.New("bad")) })
	time.AfterFunc(20*time.Millisecond, cancel)
	if c.process(ctx, record("x", nil)) {
		t.Fatal("process reported done although the dead letter was never written")
	}
}

// 配置了重试队列时，原地尝试 RetryAttempts 次仍失败，就转入重试队列并跳过原记录。
func TestTransientFailureMovesToRetryTopic(t *testing.T) {
	c, s := newTestConsumer()
	base := time.UnixMilli(1_000_000)
	c.now = func() time.Time { return base }
	c.retryTopic, c.retryAttempts, c.retryDelay = "marschat.g.retry", 3, 10*time.Second
	h, calls := failing(100, errors.New("redis down"))
	c.Handle("x", h)
	if !c.process(context.Background(), record("x", []byte("v"))) {
		t.Fatal("process returned false")
	}
	if *calls != 3 {
		t.Fatalf("handler calls = %d, want 3", *calls)
	}
	recs := s.records()
	if len(recs) != 1 {
		t.Fatalf("records written = %d, want 1", len(recs))
	}
	r := recs[0]
	if r.Topic != "marschat.g.retry" || string(r.Key) != "k" || string(r.Value) != "v" || Header(r, HeaderEventID) != "svc:1" {
		t.Errorf("retry record = %s key=%q value=%q id=%q", r.Topic, r.Key, r.Value, Header(r, HeaderEventID))
	}
	if got, want := Header(r, HeaderRetryNotBefore), strconv.FormatInt(base.Add(10*time.Second).UnixMilli(), 10); got != want {
		t.Errorf("not-before = %s, want %s", got, want)
	}
	if got := Header(r, HeaderRetryFrom); got != "t/2/41" {
		t.Errorf("retry-from = %q, want t/2/41", got)
	}
}

func TestRetryTopicNotUsedWhenRetrySucceeds(t *testing.T) {
	c, s := newTestConsumer()
	c.retryTopic, c.retryAttempts, c.retryDelay = "marschat.g.retry", 3, time.Second
	h, calls := failing(2, errors.New("redis down"))
	c.Handle("x", h)
	if !c.process(context.Background(), record("x", nil)) {
		t.Fatal("process returned false")
	}
	if *calls != 3 || len(s.records()) != 0 {
		t.Fatalf("calls = %d, written = %d; want 3 calls and nothing forwarded", *calls, len(s.records()))
	}
}

// 写不进重试队列时留在原地继续重试。
func TestRetryTopicWriteFailureKeepsRetryingInPlace(t *testing.T) {
	c, s := newTestConsumer()
	c.retryTopic, c.retryAttempts, c.retryDelay = "marschat.g.retry", 2, time.Second
	s.fail, s.failN = errors.New("broker down"), 2
	h, calls := failing(100, errors.New("redis down"))
	c.Handle("x", h)
	if !c.process(context.Background(), record("x", nil)) {
		t.Fatal("process returned false")
	}
	// 第 2、3 次失败后写入失败，第 4 次失败后写入成功
	if *calls != 4 || len(s.records()) != 1 {
		t.Fatalf("calls = %d, written = %d; want 4 calls and 1 forwarded record", *calls, len(s.records()))
	}
}

func retryRecordDueIn(d time.Duration) *kgo.Record {
	r := record("x", nil)
	r.Topic = "marschat.g.retry"
	r.Headers = append(r.Headers, kgo.RecordHeader{
		Key: HeaderRetryNotBefore, Value: []byte(strconv.FormatInt(time.Now().Add(d).UnixMilli(), 10)),
	})
	return r
}

func newRetryConsumer(delay time.Duration) (*Consumer, *sink) {
	c, s := newTestConsumer()
	c.group, c.delayed, c.retryDelay = "g-retry", true, delay
	return c, s
}

// 重试消费者等到记录头中的时间才处理。
func TestRetryConsumerWaitsUntilDue(t *testing.T) {
	c, _ := newRetryConsumer(10 * time.Second)
	var handledAt time.Time
	c.Handle("x", func(context.Context, *Event) error { handledAt = time.Now(); return nil })
	start := time.Now()
	if !c.process(context.Background(), retryRecordDueIn(150*time.Millisecond)) {
		t.Fatal("process returned false")
	}
	if d := handledAt.Sub(start); d < 100*time.Millisecond {
		t.Fatalf("handled after %v, want to wait until the record is due", d)
	}
}

// 等待最多 retryDelay：时钟偏差导致记录头的时间很远时，不会一直挡住分区。
func TestRetryConsumerWaitIsCapped(t *testing.T) {
	c, _ := newRetryConsumer(50 * time.Millisecond)
	c.Handle("x", func(context.Context, *Event) error { return nil })
	start := time.Now()
	if !c.process(context.Background(), retryRecordDueIn(time.Hour)) {
		t.Fatal("process returned false")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v, want at most the retry delay", d)
	}
}

func TestRetryConsumerWaitInterruptedByShutdown(t *testing.T) {
	c, _ := newRetryConsumer(10 * time.Second)
	c.Handle("x", func(context.Context, *Event) error { t.Error("handler called during shutdown"); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if c.process(ctx, retryRecordDueIn(5*time.Second)) {
		t.Fatal("process reported done although it was interrupted")
	}
}

// 重试消费者原地无限重试临时错误（Redis 故障期间不把正常事件送进死信队列），Permanent 错误才进死信队列。
func TestRetryConsumerRetriesInPlaceAndDeadLettersPermanent(t *testing.T) {
	c, s := newRetryConsumer(time.Second)
	h, calls := failing(12, errors.New("redis down"))
	c.Handle("x", h)
	if !c.process(context.Background(), retryRecordDueIn(0)) {
		t.Fatal("process returned false")
	}
	if *calls != 13 || len(s.records()) != 0 {
		t.Fatalf("calls = %d, written = %d; want 13 calls and nothing written", *calls, len(s.records()))
	}

	c.Handle("perm", func(context.Context, *Event) error { return Permanent(errors.New("bad")) })
	r := retryRecordDueIn(0)
	r.Headers[0].Value = []byte("perm")
	if !c.process(context.Background(), r) {
		t.Fatal("process returned false")
	}
	recs := s.records()
	if len(recs) != 1 || recs[0].Topic != TopicDLQ {
		t.Fatalf("written = %v, want one dead letter", recs)
	}
	if got := Header(recs[0], HeaderDLQGroup); got != "g-retry" {
		t.Errorf("dead letter group = %q, want g-retry", got)
	}
	if got := Header(recs[0], HeaderRetryNotBefore); got != "" {
		t.Errorf("dead letter still carries %s = %q", HeaderRetryNotBefore, got)
	}
}

// countingHandler 统计各级别的日志条数。
type countingHandler struct {
	mu     sync.Mutex
	counts map[slog.Level]int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[r.Level]++
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

// 连续失败 10 次后改记 ERROR，之后同一条记录的失败日志限频，避免刷屏。
func TestRepeatedFailuresEscalateToRateLimitedError(t *testing.T) {
	c, _ := newTestConsumer()
	logs := &countingHandler{counts: map[slog.Level]int{}}
	c.logger = slog.New(logs)
	h, _ := failing(15, errors.New("db down"))
	c.Handle("x", h)
	if !c.process(context.Background(), record("x", nil)) {
		t.Fatal("process returned false")
	}
	if logs.counts[slog.LevelWarn] != escalateAfter-1 || logs.counts[slog.LevelError] != 1 {
		t.Fatalf("warn = %d, error = %d; want %d warnings then 1 error",
			logs.counts[slog.LevelWarn], logs.counts[slog.LevelError], escalateAfter-1)
	}
}

func TestProcessIgnoresUnknownType(t *testing.T) {
	c, s := newTestConsumer()
	if !c.process(context.Background(), record("unknown", nil)) {
		t.Fatal("unknown type should be skipped")
	}
	if len(s.records()) != 0 {
		t.Fatal("unknown type written somewhere")
	}
}

func TestProcessStopsWhenCanceled(t *testing.T) {
	c, _ := newTestConsumer()
	ctx, cancel := context.WithCancel(context.Background())
	c.Handle("x", func(context.Context, *Event) error {
		cancel()
		return errors.New("fail")
	})
	if c.process(ctx, record("x", nil)) {
		t.Fatal("process should report unfinished record on shutdown")
	}
}

func TestOnDecodesProto(t *testing.T) {
	c, _ := newTestConsumer()
	var got string
	On(c, func(_ context.Context, _ *Event, msg *wrapperspb.StringValue) error {
		got = msg.GetValue()
		return nil
	})
	payload, err := proto.Marshal(wrapperspb.String("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.process(context.Background(), record("google.protobuf.StringValue", payload)) {
		t.Fatal("process returned false")
	}
	if got != "hello" {
		t.Fatalf("decoded %q, want hello", got)
	}
}

// 配置重试队列时创建重试消费者：消费组加 -retry 后缀，与主消费者共用处理器，参数取默认值。
func TestNewConsumerWithRetryTopic(t *testing.T) {
	// 创建客户端不会连接 broker
	c, cleanup, err := NewConsumer(&conf.Kafka{Brokers: "127.0.0.1:1"}, ConsumerConfig{
		Group: "marschat-g", Topics: []string{"t"}, RetryTopic: "marschat.g.retry",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c.Handle("x", func(context.Context, *Event) error { return nil })
	r := c.retry
	if r == nil || r.group != "marschat-g-retry" || !r.delayed || r.retryTopic != "" {
		t.Fatalf("retry consumer = %+v", r)
	}
	if _, ok := r.handlers["x"]; !ok {
		t.Error("retry consumer does not share the handlers")
	}
	if c.retryAttempts != defaultRetryAttempts || c.retryDelay != defaultRetryDelay || r.retryDelay != defaultRetryDelay {
		t.Errorf("attempts = %d, delay = %v / %v; want defaults", c.retryAttempts, c.retryDelay, r.retryDelay)
	}
}
