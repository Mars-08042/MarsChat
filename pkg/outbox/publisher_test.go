package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeStore struct {
	mu      sync.Mutex
	events  []Event
	deletes int
}

func (s *fakeStore) add(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range n {
		s.events = append(s.events, Event{ID: int64(len(s.events) + 1), Topic: "t", EventKey: "k"})
	}
}

func (s *fakeStore) fetch(_ context.Context, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, e := range s.events {
		if e.PublishedAt == nil && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *fakeStore) markPublished(_ context.Context, ids []int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.events[id-1].PublishedAt = &at
	}
	return nil
}

func (s *fakeStore) deletePublished(_ context.Context, _ time.Time, limit int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	if s.deletes == 1 {
		return int64(limit), nil
	}
	return 3, nil
}

func (s *fakeStore) unpublished() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.PublishedAt == nil {
			n++
		}
	}
	return n
}

type fakeProducer struct {
	mu      sync.Mutex
	err     error
	batches [][]int64
}

func (p *fakeProducer) produce(_ context.Context, events []Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	ids := make([]int64, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	p.batches = append(p.batches, ids)
	return nil
}

func (p *fakeProducer) published() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var all []int64
	for _, b := range p.batches {
		all = append(all, b...)
	}
	return all
}

type fixture struct {
	mr  *miniredis.Miniredis
	st  *fakeStore
	pr  *fakeProducer
	pub *Publisher
}

func newFixture(t *testing.T, batch int) *fixture {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	st, pr := &fakeStore{}, &fakeProducer{}
	opts := Options{Source: "svc", Owner: "me", PollInterval: time.Hour, BatchSize: batch, Retention: time.Hour}
	pub := newPublisher(st, pr, rdb, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &fixture{mr: mr, st: st, pr: pr, pub: pub}
}

func TestDrainPublishesInOrderAcrossBatches(t *testing.T) {
	f := newFixture(t, 2)
	f.st.add(5)
	ctx := context.Background()
	f.pub.maintainLease(ctx)
	f.pub.drain(ctx)

	got := f.pr.published()
	want := []int64{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("published %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("published %v, want %v", got, want)
		}
	}
	if len(f.pr.batches) != 3 {
		t.Errorf("batches = %d, want 3", len(f.pr.batches))
	}
	if n := f.st.unpublished(); n != 0 {
		t.Errorf("unpublished = %d, want 0", n)
	}
}

func TestNoPublishWithoutLease(t *testing.T) {
	f := newFixture(t, 10)
	f.st.add(3)
	if err := f.mr.Set(f.pub.leaseKey(), "other"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f.pub.maintainLease(ctx)
	f.pub.drain(ctx)
	if got := f.pr.published(); len(got) != 0 {
		t.Fatalf("published %v without lease", got)
	}
}

func TestProduceFailureBacksOff(t *testing.T) {
	f := newFixture(t, 10)
	f.st.add(2)
	clock := time.Now()
	f.pub.now = func() time.Time { return clock }
	ctx := context.Background()
	f.pub.maintainLease(ctx)

	f.pr.err = errors.New("broker down")
	f.pub.drain(ctx)
	if f.pub.backoff != minBackoff {
		t.Fatalf("backoff = %v, want %v", f.pub.backoff, minBackoff)
	}
	f.pub.drain(ctx) // 退避期内不再尝试
	if f.pub.backoff != minBackoff {
		t.Fatalf("retried during backoff, backoff = %v", f.pub.backoff)
	}

	f.pr.err = nil
	clock = clock.Add(minBackoff)
	f.pub.drain(ctx)
	if n := f.st.unpublished(); n != 0 {
		t.Fatalf("unpublished after recovery = %d, want 0", n)
	}
	if f.pub.backoff != 0 {
		t.Errorf("backoff not reset: %v", f.pub.backoff)
	}
}

func TestBackoffCapped(t *testing.T) {
	f := newFixture(t, 10)
	for range 10 {
		f.pub.fail("op", errors.New("x"))
	}
	if f.pub.backoff != maxBackoff {
		t.Fatalf("backoff = %v, want %v", f.pub.backoff, maxBackoff)
	}
}

func TestLeaseLostStopsPublishing(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()
	f.pub.maintainLease(ctx)
	if !f.pub.holding() {
		t.Fatal("lease not acquired")
	}
	// 模拟租约过期后被别的实例拿走
	if err := f.mr.Set(f.pub.leaseKey(), "other"); err != nil {
		t.Fatal(err)
	}
	f.pub.maintainLease(ctx)
	if f.pub.holding() {
		t.Fatal("still holding after lease was taken")
	}
	f.st.add(1)
	f.pub.drain(ctx)
	if got := f.pr.published(); len(got) != 0 {
		t.Fatalf("published %v after losing lease", got)
	}
}

func TestNotifyWakesAndStopReleasesLease(t *testing.T) {
	f := newFixture(t, 10)
	go func() { _ = f.pub.Start(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	for !f.mr.Exists(f.pub.leaseKey()) {
		if time.Now().After(deadline) {
			t.Fatal("lease not acquired")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 轮询间隔是 1 小时，只有唤醒能让事件这么快发出
	f.st.add(2)
	f.pub.Notify()
	for f.st.unpublished() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("events not published after Notify")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.pub.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if f.mr.Exists(f.pub.leaseKey()) {
		t.Error("lease not released on stop")
	}
}

// 事务提交在没有租约的实例上时，经 Redis 频道唤醒持有租约的实例，不必等它下一轮轮询。
func TestNotifyOnFollowerWakesLeader(t *testing.T) {
	mr := miniredis.RunT(t)
	newRDB := func() *redis.Client {
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		return rdb
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// 两个实例共用同一张 outbox 表（同一个库），各有自己的 Kafka 生产者
	st := &fakeStore{}
	leaderPr, followerPr := &fakeProducer{}, &fakeProducer{}
	opts := Options{Source: "svc", PollInterval: time.Hour, BatchSize: 10, Retention: time.Hour}
	opts.Owner = "leader"
	leader := newPublisher(st, leaderPr, newRDB(), opts, logger)
	opts.Owner = "follower"
	follower := newPublisher(st, followerPr, newRDB(), opts, logger)

	go func() { _ = leader.Start(context.Background()) }()
	deadline := time.Now().Add(2 * time.Second)
	for !mr.Exists(leader.leaseKey()) {
		if time.Now().After(deadline) {
			t.Fatal("leader did not acquire the lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	go func() { _ = follower.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = follower.Stop(ctx)
		_ = leader.Stop(ctx)
	})
	// 两个实例都订阅了唤醒频道后再提交，否则唤醒可能在订阅生效前发出
	for mr.PubSubNumSub(leader.wakeChannel())[leader.wakeChannel()] < 2 {
		if time.Now().After(deadline) {
			t.Fatal("publishers did not subscribe to the wake-up channel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := mr.Get(leader.leaseKey()); got != "leader" {
		t.Fatalf("lease holder = %q, want leader", got)
	}

	// 轮询间隔是 1 小时，只有跨实例唤醒能让事件这么快发出
	st.add(3)
	follower.Notify()
	for st.unpublished() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("events not published after a follower's Notify")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(leaderPr.published()); n != 3 {
		t.Errorf("leader published %d events, want 3", n)
	}
	if n := len(followerPr.published()); n != 0 {
		t.Errorf("follower published %d events without the lease", n)
	}
}

// 持有租约的实例只在本进程内唤醒，不向 Redis 频道发消息。
func TestNotifyOnLeaderStaysLocal(t *testing.T) {
	f := newFixture(t, 10)
	f.pub.maintainLease(context.Background())
	if !f.pub.leader.Load() {
		t.Fatal("leader flag not set after acquiring the lease")
	}
	f.pub.Notify()
	select {
	case <-f.pub.remoteWake:
		t.Error("leader queued a cross-instance wake-up")
	default:
	}
	f.pub.releaseLease()
	if f.pub.leader.Load() {
		t.Error("leader flag still set after releasing the lease")
	}
}

func TestToRecords(t *testing.T) {
	old := time.Now().Add(-24 * time.Hour)
	recs := toRecords("svc", []Event{{ID: 7, Topic: "t", EventKey: "k", EventType: "a.B", Payload: []byte("p"), CreatedAt: old}})
	r := recs[0]
	// 填创建时间会让积压的旧事件被 franz-go 立即判定为投递超时，见 toRecords 的注释
	if !r.Timestamp.IsZero() {
		t.Errorf("record timestamp = %v, want zero", r.Timestamp)
	}
	headers := map[string]string{}
	for _, h := range r.Headers {
		headers[h.Key] = string(h.Value)
	}
	if r.Topic != "t" || string(r.Key) != "k" || string(r.Value) != "p" ||
		headers["event_type"] != "a.B" || headers["event_id"] != "svc:7" || headers["source"] != "svc" {
		t.Errorf("unexpected record %+v headers %v", r, headers)
	}
}

func TestCleanupOnlyWhenHolding(t *testing.T) {
	f := newFixture(t, 10)
	ctx := context.Background()
	f.pub.cleanup(ctx)
	if f.st.deletes != 0 {
		t.Fatalf("cleaned up without lease")
	}
	f.pub.maintainLease(ctx)
	f.pub.cleanup(ctx)
	// 第一批删满继续删，第二批不满就停
	if f.st.deletes != 2 {
		t.Fatalf("delete calls = %d, want 2", f.st.deletes)
	}
}
