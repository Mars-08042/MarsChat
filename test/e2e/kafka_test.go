//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/kafka"
)

// 集成测试专用的 topic，由 kafka-init 创建，业务服务不消费它。
const e2eTopic = "marschat.e2e.events"

func kafkaConf() *pkgconf.Kafka {
	brokers := os.Getenv("MARSCHAT_KAFKA_BROKERS")
	if brokers == "" {
		brokers = "127.0.0.1:19092"
	}
	return &pkgconf.Kafka{Brokers: brokers}
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// collector 收集消费到的事件，供测试等待和断言。
type collector struct {
	mu     sync.Mutex
	events []*kafka.Event
}

func (c *collector) add(e *kafka.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) snapshot() []*kafka.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*kafka.Event(nil), c.events...)
}

func (c *collector) waitFor(t *testing.T, n int, timeout time.Duration) []*kafka.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got := c.snapshot()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d events, want %d", len(got), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startConsumer 启动消费者，register 注册处理器；返回的 stop 等待消费者提交并退出。
func startConsumer(t *testing.T, cc kafka.ConsumerConfig, register func(c *kafka.Consumer)) func() {
	t.Helper()
	c, cleanup, err := kafka.NewConsumer(kafkaConf(), cc, quiet)
	if err != nil {
		t.Fatal(err)
	}
	register(c)
	go func() { _ = c.Start(context.Background()) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = c.Stop(ctx)
			cleanup()
		})
	}
	t.Cleanup(stop)
	return stop
}

func produceTestEvents(t *testing.T, cl *kgo.Client, key string, from, to int) {
	t.Helper()
	var recs []*kgo.Record
	for i := from; i < to; i++ {
		recs = append(recs, &kgo.Record{
			Topic: e2eTopic, Key: []byte(key), Value: []byte(strconv.Itoa(i)),
			Headers: []kgo.RecordHeader{
				{Key: kafka.HeaderEventType, Value: []byte("e2e.Test")},
				{Key: kafka.HeaderEventID, Value: []byte(key + ":" + strconv.Itoa(i))},
			},
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		t.Fatalf("produce: %v", err)
	}
}

// TestKafkaConsumeCommitAndResume 验证经宿主机端口 19092 收发、消费组按处理进度提交位点，
// 重启后从提交的位点继续，不重复处理已处理的记录。
func TestKafkaConsumeCommitAndResume(t *testing.T) {
	run := randomHex(t)
	cl, closeProducer, err := kafka.NewProducer(kafkaConf(), "e2e")
	if err != nil {
		t.Fatal(err)
	}
	defer closeProducer()

	cc := kafka.ConsumerConfig{Group: "e2e-" + run, Topics: []string{e2eTopic}}
	register := func(col *collector) func(c *kafka.Consumer) {
		return func(c *kafka.Consumer) {
			c.Handle("e2e.Test", func(_ context.Context, e *kafka.Event) error {
				// topic 里有之前运行留下的记录，只收集本次的
				if e.Key == run {
					col.add(e)
				}
				return nil
			})
		}
	}

	produceTestEvents(t, cl, run, 0, 3)
	first := &collector{}
	stop := startConsumer(t, cc, register(first))
	got := first.waitFor(t, 3, 30*time.Second)
	for i, e := range got {
		if string(e.Payload) != strconv.Itoa(i) || e.ID != run+":"+strconv.Itoa(i) {
			t.Fatalf("event %d = %q (%s), out of order or wrong id", i, e.Payload, e.ID)
		}
	}
	stop()

	produceTestEvents(t, cl, run, 3, 5)
	second := &collector{}
	startConsumer(t, cc, register(second))
	got = second.waitFor(t, 2, 30*time.Second)
	// 多等一会儿，确认已提交的记录不会再次投递
	time.Sleep(time.Second)
	got = second.snapshot()
	if len(got) != 2 || string(got[0].Payload) != "3" || string(got[1].Payload) != "4" {
		var payloads []string
		for _, e := range got {
			payloads = append(payloads, string(e.Payload))
		}
		t.Fatalf("after restart got %v, want [3 4]", payloads)
	}
}

// e2eRetryTopic 是集成测试专用的重试队列，由 kafka-init 创建。
const e2eRetryTopic = "marschat.e2e.retry"

// TestKafkaRetryTopicAndDeadLetter 验证重试队列与死信队列经真实 Kafka 的往返：一直临时失败的事件
// 在主 topic 上尝试 RetryAttempts 次后转入重试队列，延迟后由重试消费者处理成功；
// Permanent 失败的事件写入死信队列，带上原始的事件 ID 和原位置。
func TestKafkaRetryTopicAndDeadLetter(t *testing.T) {
	run := randomHex(t)
	cl, closeProducer, err := kafka.NewProducer(kafkaConf(), "e2e")
	if err != nil {
		t.Fatal(err)
	}
	defer closeProducer()

	group := "e2e-retry-" + run
	var mu sync.Mutex
	mainAttempts := 0
	retried := &collector{}
	startConsumer(t, kafka.ConsumerConfig{
		Group: group, Topics: []string{e2eTopic},
		RetryTopic: e2eRetryTopic, RetryAttempts: 2, RetryDelay: time.Second,
	}, func(c *kafka.Consumer) {
		c.Handle("e2e.Test", func(_ context.Context, e *kafka.Event) error {
			// topic 里有之前运行留下的记录，只处理本次的
			if e.Key != run {
				return nil
			}
			switch string(e.Payload) {
			case "transient":
				if e.Topic == e2eTopic {
					mu.Lock()
					mainAttempts++
					mu.Unlock()
					return errors.New("simulated outage")
				}
				retried.add(e)
				return nil
			case "permanent":
				return kafka.Permanent(errors.New("simulated bad content"))
			}
			return nil
		})
	})

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	recs := []*kgo.Record{
		{Topic: e2eTopic, Key: []byte(run), Value: []byte("transient"), Headers: []kgo.RecordHeader{
			{Key: kafka.HeaderEventType, Value: []byte("e2e.Test")}, {Key: kafka.HeaderEventID, Value: []byte(run + ":transient")},
		}},
		{Topic: e2eTopic, Key: []byte(run), Value: []byte("permanent"), Headers: []kgo.RecordHeader{
			{Key: kafka.HeaderEventType, Value: []byte("e2e.Test")}, {Key: kafka.HeaderEventID, Value: []byte(run + ":permanent")},
		}},
	}
	if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		t.Fatalf("produce: %v", err)
	}

	got := retried.waitFor(t, 1, 60*time.Second)[0]
	if got.Topic != e2eRetryTopic || got.ID != run+":transient" {
		t.Fatalf("retried event = %+v, want it from %s with the original id", got, e2eRetryTopic)
	}
	mu.Lock()
	attempts := mainAttempts
	mu.Unlock()
	if attempts != 2 {
		t.Errorf("attempts on the main topic = %d, want 2 (RetryAttempts)", attempts)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("retried after %v, want at least the 1s retry delay", d)
	}

	// 从测试开始的时间点读死信队列，找到本次的记录
	dlq, err := kgo.NewClient(kgo.SeedBrokers(kafka.Brokers(kafkaConf())...), kgo.ConsumeTopics(kafka.TopicDLQ),
		kgo.ConsumeResetOffset(kgo.NewOffset().AfterMilli(start.Add(-time.Second).UnixMilli())))
	if err != nil {
		t.Fatal(err)
	}
	defer dlq.Close()
	pollCtx, pollCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pollCancel()
	for {
		fetches := dlq.PollFetches(pollCtx)
		if pollCtx.Err() != nil {
			t.Fatal("dead letter not found in marschat.dlq within 30s")
		}
		var found *kgo.Record
		fetches.EachRecord(func(r *kgo.Record) {
			if string(r.Key) == run {
				found = r
			}
		})
		if found == nil {
			continue
		}
		want := map[string]string{
			kafka.HeaderEventID: run + ":permanent", kafka.HeaderDLQGroup: group, kafka.HeaderDLQTopic: e2eTopic,
		}
		for k, v := range want {
			if got := kafka.Header(found, k); got != v {
				t.Errorf("dead letter header %s = %q, want %q", k, got, v)
			}
		}
		if string(found.Value) != "permanent" || !strings.Contains(kafka.Header(found, kafka.HeaderDLQError), "simulated bad content") {
			t.Errorf("dead letter value %q, error %q", found.Value, kafka.Header(found, kafka.HeaderDLQError))
		}
		return
	}
}

// TestOutboxPublishesServiceEvents 验证 account 和 social 的 Outbox 发布器把事务里写入的事件发到 Kafka。
func TestOutboxPublishesServiceEvents(t *testing.T) {
	resetRateLimits(t)
	a, b := newUser(t), newUser(t)

	friendRequests, revocations := &collector{}, &collector{}
	startConsumer(t, kafka.ConsumerConfig{
		Group:  "e2e-outbox-" + randomHex(t),
		Topics: []string{kafka.TopicSocialEvents, kafka.TopicAccountEvents},
	}, func(c *kafka.Consumer) {
		kafka.On(c, func(_ context.Context, e *kafka.Event, ev *socialv1.FriendRequestCreated) error {
			if ev.GetToUserId() == b.id {
				friendRequests.add(e)
			}
			return nil
		})
		kafka.On(c, func(_ context.Context, e *kafka.Event, ev *accountv1.SessionRevoked) error {
			if ev.GetUserId() == a.id && ev.GetReason() == accountv1.RevokeReason_REVOKE_REASON_LOGOUT {
				revocations.add(e)
			}
			return nil
		})
	})

	mustSocial(t, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: b.id}, nil)
	mustCall(t, http.MethodPost, "/v1/auth/logout", a.token, &accountv1.LogoutRequest{}, nil)

	fr := friendRequests.waitFor(t, 1, 30*time.Second)[0]
	if fr.Key != id(b.id) || !strings.HasPrefix(fr.ID, "marschat-social:") || fr.Source != "marschat-social" {
		t.Errorf("friend request event key=%s id=%s source=%s", fr.Key, fr.ID, fr.Source)
	}
	rv := revocations.waitFor(t, 1, 30*time.Second)[0]
	if rv.Key != id(a.id) || !strings.HasPrefix(rv.ID, "marschat-account:") {
		t.Errorf("session revoked event key=%s id=%s", rv.Key, rv.ID)
	}
}
