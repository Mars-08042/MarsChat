package route

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func TestRegisterListUnregister(t *testing.T) {
	_, rdb := newRedis(t)
	ctx := context.Background()
	a := Entry{ConnID: 11, SessionID: 7, Addr: "10.0.0.1:19100"}
	b := Entry{ConnID: 12, SessionID: 8, Addr: "10.0.0.2:19100"}
	for _, e := range []Entry{a, b} {
		if err := Register(ctx, rdb, 42, e, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(ctx, rdb, []int64{42, 43})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[42]) != 2 {
		t.Fatalf("List = %+v, want 2 entries for user 42 only", got)
	}
	found := map[int64]Entry{}
	for _, e := range got[42] {
		found[e.ConnID] = e
	}
	if found[11] != a || found[12] != b {
		t.Fatalf("entries = %+v", found)
	}

	if err := Unregister(ctx, rdb, 42, 11); err != nil {
		t.Fatal(err)
	}
	got, _ = List(ctx, rdb, []int64{42})
	if len(got[42]) != 1 || got[42][0] != b {
		t.Fatalf("after unregister = %+v, want only conn 12", got)
	}
}

// 字段级 TTL：一条连接停止续期后过期，同一用户其他连接的续期不影响它。
func TestFieldTTL(t *testing.T) {
	mr, rdb := newRedis(t)
	ctx := context.Background()
	if err := Register(ctx, rdb, 42, Entry{ConnID: 11, SessionID: 7, Addr: "a:1"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := Register(ctx, rdb, 42, Entry{ConnID: 12, SessionID: 7, Addr: "a:1"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(6 * time.Second)
	// 只有连接 12 续期
	if err := Register(ctx, rdb, 42, Entry{ConnID: 12, SessionID: 7, Addr: "a:1"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(6 * time.Second)
	got, err := List(ctx, rdb, []int64{42})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[42]) != 1 || got[42][0].ConnID != 12 {
		t.Fatalf("List = %+v, want only conn 12", got)
	}
}

func TestListSkipsMalformed(t *testing.T) {
	_, rdb := newRedis(t)
	ctx := context.Background()
	if err := Register(ctx, rdb, 42, Entry{ConnID: 11, SessionID: 7, Addr: "a:1"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	rdb.HSet(ctx, Key(42), "bad", "x")
	got, err := List(ctx, rdb, []int64{42})
	if err == nil {
		t.Fatal("want decode error")
	}
	if len(got[42]) != 1 || got[42][0].ConnID != 11 {
		t.Fatalf("List = %+v, want the valid entry kept", got)
	}
}
