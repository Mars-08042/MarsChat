package kafka

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func newTestConsumer() *Consumer {
	return &Consumer{
		handlers: make(map[string]Handler),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		retryMin: time.Millisecond,
		retryMax: 4 * time.Millisecond,
	}
}

func record(eventType string, payload []byte) *kgo.Record {
	return &kgo.Record{
		Topic: "t", Key: []byte("k"), Value: payload,
		Headers: []kgo.RecordHeader{
			{Key: HeaderEventType, Value: []byte(eventType)},
			{Key: HeaderEventID, Value: []byte("svc:1")},
			{Key: HeaderSource, Value: []byte("svc")},
		},
	}
}

func TestProcessRetriesUntilSuccess(t *testing.T) {
	c := newTestConsumer()
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
}

func TestProcessSkipsPermanentAndPanic(t *testing.T) {
	c := newTestConsumer()
	calls := 0
	c.Handle("perm", func(context.Context, *Event) error { calls++; return Permanent(errors.New("bad")) })
	c.Handle("panic", func(context.Context, *Event) error { calls++; panic("boom") })
	for _, typ := range []string{"perm", "panic"} {
		if !c.process(context.Background(), record(typ, nil)) {
			t.Fatalf("%s: process returned false", typ)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (no retries)", calls)
	}
}

func TestProcessIgnoresUnknownType(t *testing.T) {
	c := newTestConsumer()
	if !c.process(context.Background(), record("unknown", nil)) {
		t.Fatal("unknown type should be skipped")
	}
}

func TestProcessStopsWhenCanceled(t *testing.T) {
	c := newTestConsumer()
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
	c := newTestConsumer()
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

	// 解码失败不重试
	got = ""
	if !c.process(context.Background(), record("google.protobuf.StringValue", []byte{0xff})) {
		t.Fatal("process returned false")
	}
	if got != "" {
		t.Fatal("handler called with undecodable payload")
	}
}
