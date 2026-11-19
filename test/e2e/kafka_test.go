//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
