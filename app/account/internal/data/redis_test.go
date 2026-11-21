package data

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func TestSendSlot(t *testing.T) {
	mr, rdb := newRedis(t)
	s := NewCodeStore(rdb)
	ctx := context.Background()
	const email, day = "user1@example.com", "20261118"

	res, _, err := s.AcquireSendSlot(ctx, email, day, time.Minute, 2)
	if err != nil || res != biz.SlotOK {
		t.Fatalf("first acquire = %v, %v", res, err)
	}
	res, retry, _ := s.AcquireSendSlot(ctx, email, day, time.Minute, 2)
	if res != biz.SlotTooFrequent || retry <= 0 || retry > time.Minute {
		t.Fatalf("second acquire = %v, retry %v", res, retry)
	}

	// 发送失败撤销后可以立即重试，计数也退回
	if err := s.ReleaseSendSlot(ctx, email, day); err != nil {
		t.Fatal(err)
	}
	if got, _ := rdb.Get(ctx, dailyKey(email, day)).Int(); got != 0 {
		t.Fatalf("daily count after release = %d", got)
	}
	if res, _, _ := s.AcquireSendSlot(ctx, email, day, time.Minute, 2); res != biz.SlotOK {
		t.Fatalf("acquire after release = %v", res)
	}

	mr.FastForward(time.Minute)
	if res, _, _ := s.AcquireSendSlot(ctx, email, day, time.Minute, 2); res != biz.SlotOK {
		t.Fatalf("acquire after cooldown = %v", res)
	}
	mr.FastForward(time.Minute)
	if res, _, _ := s.AcquireSendSlot(ctx, email, day, time.Minute, 2); res != biz.SlotDailyLimit {
		t.Fatalf("acquire over daily limit = %v", res)
	}
	if ttl := mr.TTL(dailyKey(email, day)); ttl <= 0 || ttl > dailyTTL {
		t.Fatalf("daily key ttl = %v", ttl)
	}
	// 换天后重新计数
	if res, _, _ := s.AcquireSendSlot(ctx, email, "20261119", time.Minute, 2); res != biz.SlotOK {
		t.Fatalf("acquire next day = %v", res)
	}
}

func TestVerifyCode(t *testing.T) {
	mr, rdb := newRedis(t)
	s := NewCodeStore(rdb)
	ctx := context.Background()
	const email = "user1@example.com"

	if res, _ := s.VerifyCode(ctx, 1, email, "h", 3); res != biz.VerifyMissing {
		t.Fatalf("verify without code = %v", res)
	}
	if err := s.SaveCode(ctx, 1, email, "good", time.Minute); err != nil {
		t.Fatal(err)
	}
	// 用途隔离
	if res, _ := s.VerifyCode(ctx, 2, email, "good", 3); res != biz.VerifyMissing {
		t.Fatalf("verify other purpose = %v", res)
	}
	if res, _ := s.VerifyCode(ctx, 1, email, "bad", 3); res != biz.VerifyMismatch {
		t.Fatalf("verify wrong = %v", res)
	}
	if res, _ := s.VerifyCode(ctx, 1, email, "good", 3); res != biz.VerifyOK {
		t.Fatalf("verify right = %v", res)
	}
	// 一次性
	if res, _ := s.VerifyCode(ctx, 1, email, "good", 3); res != biz.VerifyMissing {
		t.Fatalf("verify reused = %v", res)
	}

	_ = s.SaveCode(ctx, 1, email, "good", time.Minute)
	for i, want := range []biz.VerifyResult{biz.VerifyMismatch, biz.VerifyMismatch, biz.VerifyExhausted, biz.VerifyMissing} {
		if res, _ := s.VerifyCode(ctx, 1, email, "bad", 3); res != want {
			t.Fatalf("attempt %d = %v, want %v", i+1, res, want)
		}
	}

	// 重新发送覆盖旧码并清零次数
	_ = s.SaveCode(ctx, 1, email, "old", time.Minute)
	_, _ = s.VerifyCode(ctx, 1, email, "bad", 3)
	_ = s.SaveCode(ctx, 1, email, "new", time.Minute)
	if n, _ := rdb.HGet(ctx, codeKey(1, email), "n").Int(); n != 0 {
		t.Fatalf("attempts after resend = %d", n)
	}
	if res, _ := s.VerifyCode(ctx, 1, email, "old", 3); res != biz.VerifyMismatch {
		t.Fatalf("old code after resend = %v", res)
	}

	mr.FastForward(time.Minute)
	if res, _ := s.VerifyCode(ctx, 1, email, "new", 3); res != biz.VerifyMissing {
		t.Fatalf("expired code = %v", res)
	}
}

func TestLoginGuard(t *testing.T) {
	mr, rdb := newRedis(t)
	g := NewLoginGuard(rdb)
	ctx := context.Background()
	const email = "user1@example.com"
	window, lock := 15*time.Minute, 30*time.Minute

	for i := 0; i < 2; i++ {
		if err := g.RecordFailure(ctx, email, 3, window, lock); err != nil {
			t.Fatal(err)
		}
	}
	if locked, _, _ := g.Locked(ctx, email, 3); locked {
		t.Fatal("locked before reaching max failures")
	}
	if ttl := mr.TTL(loginFailKey(email)); ttl != window {
		t.Fatalf("ttl during window = %v", ttl)
	}
	_ = g.RecordFailure(ctx, email, 3, window, lock)
	locked, retry, err := g.Locked(ctx, email, 3)
	if err != nil || !locked || retry != lock {
		t.Fatalf("locked = %v, retry %v, err %v", locked, retry, err)
	}
	// 热更新调大上限后立即解除锁定
	if locked, _, _ := g.Locked(ctx, email, 5); locked {
		t.Fatal("still locked after raising max failures")
	}

	if err := g.Reset(ctx, email); err != nil {
		t.Fatal(err)
	}
	if locked, _, _ := g.Locked(ctx, email, 3); locked {
		t.Fatal("locked after reset")
	}

	_ = g.RecordFailure(ctx, email, 3, window, lock)
	mr.FastForward(window)
	if mr.Exists(loginFailKey(email)) {
		t.Fatal("failure counter not expired after window")
	}
}
