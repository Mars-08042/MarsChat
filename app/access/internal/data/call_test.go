package data

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
)

func newTestCallRepo(t *testing.T) (*CallRepo, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewCallRepo(rdb).(*CallRepo), mr
}

func ringingCall(id, caller, callee int64) *biz.Call {
	return &biz.Call{
		CallKey:    biz.CallKey{ID: id, CallerID: caller, CalleeID: callee},
		CallerConn: 1001,
		CallerAddr: "10.0.0.1:19100",
		Media:      v1.CallMedia_CALL_MEDIA_AUDIO,
		State:      biz.CallRinging,
		CreatedAt:  time.UnixMilli(1_700_000_000_000),
	}
}

func TestCallRepoLifecycle(t *testing.T) {
	ctx := context.Background()
	r, mr := newTestCallRepo(t)
	c := ringingCall(7, 42, 43)

	res, err := r.Create(ctx, c, 75*time.Second)
	if err != nil || res != biz.CreateOK {
		t.Fatalf("create = %v, %v", res, err)
	}
	for _, k := range []string{"access:call:7", "access:call_user:42", "access:call_user:43"} {
		if ttl := mr.TTL(k); ttl != 75*time.Second {
			t.Errorf("TTL %s = %v, want 75s", k, ttl)
		}
	}
	if v, _ := mr.Get("access:call_user:43"); v != "7" {
		t.Errorf("busy marker of callee = %q, want 7", v)
	}

	// 任何一方已在通话中都不能再创建
	if res, _ := r.Create(ctx, ringingCall(8, 42, 44), time.Minute); res != biz.CreateCallerBusy {
		t.Errorf("caller busy: got %v", res)
	}
	if res, _ := r.Create(ctx, ringingCall(9, 44, 43), time.Minute); res != biz.CreateCalleeBusy {
		t.Errorf("callee busy: got %v", res)
	}
	if mr.Exists("access:call_user:44") {
		t.Error("busy marker created for a failed call")
	}

	got, err := r.Get(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.CallerID != 42 || got.CalleeID != 43 || got.CallerConn != 1001 || got.CallerAddr != "10.0.0.1:19100" ||
		got.CalleeConn != 0 || got.Media != v1.CallMedia_CALL_MEDIA_AUDIO || got.State != biz.CallRinging ||
		!got.CreatedAt.Equal(c.CreatedAt) || !got.AnsweredAt.IsZero() {
		t.Errorf("get = %+v", got)
	}

	// 振铃中：主叫连接仍是通话方，但不续期
	if alive, err := r.Touch(ctx, c.CallKey, 1001, 90*time.Second); !alive || err != nil {
		t.Errorf("touch while ringing = %v, %v", alive, err)
	}
	if ttl := mr.TTL("access:call:7"); ttl != 75*time.Second {
		t.Errorf("TTL after touch while ringing = %v, want unchanged 75s", ttl)
	}

	at := time.UnixMilli(1_700_000_005_000)
	ok, _, err := r.Accept(ctx, c.CallKey, 2002, "10.0.0.2:19100", at, 90*time.Second)
	if !ok || err != nil {
		t.Fatalf("accept = %v, %v", ok, err)
	}
	for _, k := range []string{"access:call:7", "access:call_user:42", "access:call_user:43"} {
		if ttl := mr.TTL(k); ttl != 90*time.Second {
			t.Errorf("TTL %s after accept = %v, want 90s", k, ttl)
		}
	}
	if ok, cur, _ := r.Accept(ctx, c.CallKey, 2003, "10.0.0.3:19100", at, time.Minute); ok || cur != biz.CallActive {
		t.Errorf("second accept = %v, %q, want false, active", ok, cur)
	}

	// 通话中：双方连接续期，其他连接不是通话方
	mr.FastForward(30 * time.Second)
	if alive, _ := r.Touch(ctx, c.CallKey, 2002, 90*time.Second); !alive {
		t.Error("callee conn not alive")
	}
	if ttl := mr.TTL("access:call_user:42"); ttl != 90*time.Second {
		t.Errorf("TTL after touch = %v, want 90s", ttl)
	}
	if alive, _ := r.Touch(ctx, c.CallKey, 2003, 90*time.Second); alive {
		t.Error("non-participant conn reported alive")
	}

	// 条件不满足时不删除，返回当前状态
	if ended, cur, err := r.End(ctx, c.CallKey, biz.EndCond{By: biz.PartyCallee, ID: 43, State: biz.CallRinging}); ended != nil || cur != biz.CallActive || err != nil {
		t.Errorf("reject while active = %v, %q, %v", ended, cur, err)
	}
	if ended, cur, _ := r.End(ctx, c.CallKey, biz.EndCond{By: biz.PartyCalleeConn, ID: 2003}); ended != nil || cur != biz.CallActive {
		t.Errorf("end by wrong conn = %v, %q", ended, cur)
	}

	ended, _, err := r.End(ctx, c.CallKey, biz.EndCond{By: biz.PartyCalleeConn, ID: 2002, State: biz.CallActive})
	if err != nil || ended == nil {
		t.Fatalf("end = %v, %v", ended, err)
	}
	if ended.State != biz.CallActive || ended.CalleeConn != 2002 || ended.CalleeAddr != "10.0.0.2:19100" || !ended.AnsweredAt.Equal(at) {
		t.Errorf("ended snapshot = %+v", ended)
	}
	for _, k := range []string{"access:call:7", "access:call_user:42", "access:call_user:43"} {
		if mr.Exists(k) {
			t.Errorf("%s left after end", k)
		}
	}
	if got, err := r.Get(ctx, 7); got != nil || err != nil {
		t.Errorf("get after end = %v, %v", got, err)
	}
	if ended, cur, err := r.End(ctx, c.CallKey, biz.EndCond{}); ended != nil || cur != "" || err != nil {
		t.Errorf("end missing = %v, %q, %v", ended, cur, err)
	}
	if ok, cur, err := r.Accept(ctx, c.CallKey, 2002, "x", at, time.Minute); ok || cur != "" || err != nil {
		t.Errorf("accept missing = %v, %q, %v", ok, cur, err)
	}
}

// 振铃超时只结束仍在振铃的通话；拒绝要求发起方是被叫用户。
func TestCallRepoEndConditions(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestCallRepo(t)
	c := ringingCall(7, 42, 43)
	if _, err := r.Create(ctx, c, time.Minute); err != nil {
		t.Fatal(err)
	}
	if ended, cur, _ := r.End(ctx, c.CallKey, biz.EndCond{By: biz.PartyCallee, ID: 42, State: biz.CallRinging}); ended != nil || cur != biz.CallRinging {
		t.Errorf("reject by caller = %v, %q", ended, cur)
	}
	// 振铃中没有接听连接，按接听连接结束不会误中
	if ended, _, _ := r.End(ctx, c.CallKey, biz.EndCond{By: biz.PartyCalleeConn, ID: 0}); ended != nil {
		t.Error("ended by callee_conn 0 while ringing")
	}
	ended, _, err := r.End(ctx, c.CallKey, biz.EndCond{By: biz.PartyAny, State: biz.CallRinging})
	if err != nil || ended == nil || ended.State != biz.CallRinging {
		t.Fatalf("timeout end = %v, %v", ended, err)
	}
}

// 忙线标记已指向另一通通话时，结束和续期都不碰它。
func TestCallRepoKeepsOtherBusyMarker(t *testing.T) {
	ctx := context.Background()
	r, mr := newTestCallRepo(t)
	c := ringingCall(7, 42, 43)
	if _, err := r.Create(ctx, c, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Accept(ctx, c.CallKey, 2002, "a", time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := mr.Set("access:call_user:43", "8"); err != nil {
		t.Fatal(err)
	}
	if alive, _ := r.Touch(ctx, c.CallKey, 1001, 2*time.Minute); !alive {
		t.Fatal("caller conn not alive")
	}
	if mr.TTL("access:call_user:43") != 0 {
		t.Error("touch extended a busy marker of another call")
	}
	if ended, _, _ := r.End(ctx, c.CallKey, biz.EndCond{By: biz.PartyCallerConn, ID: 1001}); ended == nil {
		t.Fatal("end failed")
	}
	if v, _ := mr.Get("access:call_user:43"); v != "8" {
		t.Errorf("busy marker of another call = %q, want 8 kept", v)
	}
	if mr.Exists("access:call_user:42") {
		t.Error("busy marker of this call left")
	}
}
