package biz

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

// 内存实现的存储，只模拟 biz 依赖的语义（条件更新、唯一约束等），不模拟事务回滚。

type fakeTx struct{}

func (fakeTx) InTx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type fakeUsers struct {
	mu         sync.Mutex
	byID       map[int64]*User
	registered []int64
	evicted    []int64
}

func newFakeUsers() *fakeUsers { return &fakeUsers{byID: map[int64]*User{}} }

func (f *fakeUsers) Create(_ context.Context, u *User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.byID {
		if x.Email == u.Email {
			return ErrEmailTaken
		}
	}
	cp := *u
	f.byID[u.ID] = &cp
	return nil
}

func (f *fakeUsers) FindByID(_ context.Context, id int64) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeUsers) FindByEmail(_ context.Context, email string) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byID {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeUsers) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	_, err := f.FindByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (f *fakeUsers) UpdateProfile(_ context.Context, id int64, upd ProfileUpdate, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil
	}
	if upd.Nickname != nil {
		u.Nickname = *upd.Nickname
	}
	if upd.SetBirthday {
		u.Birthday = upd.Birthday
	}
	u.UpdatedAt = now
	return nil
}

func (f *fakeUsers) SetStatus(_ context.Context, id int64, status int32, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok || u.Status == status {
		return false, nil
	}
	u.Status, u.UpdatedAt = status, now
	return true, nil
}

func (f *fakeUsers) LockStatus(ctx context.Context, id int64) (int32, error) {
	u, err := f.FindByID(ctx, id)
	if err != nil {
		return 0, err
	}
	return u.Status, nil
}

func (f *fakeUsers) GetCached(ctx context.Context, id int64) (*User, error) {
	u, err := f.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = ""
	return u, nil
}

func (f *fakeUsers) BatchGetCached(ctx context.Context, ids []int64) ([]*User, error) {
	var out []*User
	for _, id := range ids {
		if u, err := f.GetCached(ctx, id); err == nil {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeUsers) EvictCache(_ context.Context, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, id)
}

func (f *fakeUsers) OnRegistered(_ context.Context, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, id)
}

type fakeSessions struct {
	mu   sync.Mutex
	byID map[int64]*Session
}

func newFakeSessions() *fakeSessions { return &fakeSessions{byID: map[int64]*Session{}} }

func (f *fakeSessions) Create(_ context.Context, s *Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *s
	f.byID[s.ID] = &cp
	return nil
}

func (f *fakeSessions) FindByID(_ context.Context, id int64) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeSessions) revoke(s *Session, reason int32, at time.Time) {
	t := at
	s.RevokedAt, s.RevokeReason = &t, reason
}

func (f *fakeSessions) RevokeActiveByDevice(_ context.Context, userID int64, deviceID string, reason int32, at time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.byID {
		if s.UserID == userID && s.Device.ID == deviceID && s.RevokedAt == nil {
			f.revoke(s, reason, at)
			return s.ID, nil
		}
	}
	return 0, nil
}

func (f *fakeSessions) Revoke(_ context.Context, id, userID int64, reason int32, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok || s.UserID != userID || s.RevokedAt != nil {
		return false, nil
	}
	f.revoke(s, reason, at)
	return true, nil
}

func (f *fakeSessions) RevokeAllByUser(_ context.Context, userID int64, reason int32, at time.Time) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []int64
	for _, s := range f.byID {
		if s.UserID == userID && s.RevokedAt == nil {
			f.revoke(s, reason, at)
			ids = append(ids, s.ID)
		}
	}
	return ids, nil
}

func (f *fakeSessions) Rotate(_ context.Context, id int64, oldHash, newHash []byte, expiresAt time.Time, ip string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok || s.RevokedAt != nil || !bytes.Equal(s.RefreshHash, oldHash) {
		return false, nil
	}
	t := at
	s.PrevRefreshHash, s.RefreshHash = s.RefreshHash, newHash
	s.RotatedAt, s.RefreshExpiresAt, s.LastIP, s.LastActiveAt = &t, expiresAt, ip, at
	return true, nil
}

func (f *fakeSessions) ListActive(_ context.Context, userID int64) ([]*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Session
	for _, s := range f.byID {
		if s.UserID == userID && s.RevokedAt == nil {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}

type fakeEvents struct {
	mu   sync.Mutex
	msgs []proto.Message
}

func (f *fakeEvents) Add(_ context.Context, _, _ string, msg proto.Message, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
	return nil
}

type fakeCodeStore struct {
	mu       sync.Mutex
	slot     SlotResult
	retry    time.Duration
	codes    map[string]string
	attempts map[string]int
	released int
	// slotFor 记录占用发送名额时传入的邮箱
	slotFor []string
}

func newFakeCodeStore() *fakeCodeStore {
	return &fakeCodeStore{codes: map[string]string{}, attempts: map[string]int{}}
}

func codeKey(purpose int32, email string) string { return string(rune('0'+purpose)) + email }

func (f *fakeCodeStore) AcquireSendSlot(_ context.Context, email, _ string, _ time.Duration, _ int) (SlotResult, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slotFor = append(f.slotFor, email)
	return f.slot, f.retry, nil
}

func (f *fakeCodeStore) ReleaseSendSlot(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released++
	return nil
}

func (f *fakeCodeStore) SaveCode(_ context.Context, purpose int32, email, codeHash string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes[codeKey(purpose, email)] = codeHash
	f.attempts[codeKey(purpose, email)] = 0
	return nil
}

func (f *fakeCodeStore) DeleteCode(_ context.Context, purpose int32, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.codes, codeKey(purpose, email))
	return nil
}

func (f *fakeCodeStore) VerifyCode(_ context.Context, purpose int32, email, codeHash string, maxAttempts int) (VerifyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := codeKey(purpose, email)
	h, ok := f.codes[k]
	switch {
	case !ok:
		return VerifyMissing, nil
	case h == codeHash:
		delete(f.codes, k)
		return VerifyOK, nil
	}
	f.attempts[k]++
	if f.attempts[k] >= maxAttempts {
		delete(f.codes, k)
		return VerifyExhausted, nil
	}
	return VerifyMismatch, nil
}

type fakeGuard struct {
	mu       sync.Mutex
	failures map[string]int
}

func (f *fakeGuard) Locked(_ context.Context, email string, max int) (bool, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures[email] >= max {
		return true, time.Minute, nil
	}
	return false, 0, nil
}

func (f *fakeGuard) RecordFailure(_ context.Context, email string, _ int, _, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[email]++
	return nil
}

func (f *fakeGuard) Reset(_ context.Context, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failures, email)
	return nil
}

type fakeLimiter struct{ deny bool }

func (f *fakeLimiter) Allow(context.Context, string, int, time.Duration) (ratelimit.Result, error) {
	if f.deny {
		return ratelimit.Result{Allowed: false, RetryAfter: 30 * time.Second}, nil
	}
	return ratelimit.Result{Allowed: true}, nil
}

type fakeSender struct {
	err       error
	lastTo    string
	lastCode  string
	lastTTL   time.Duration
	sentCount int
}

func (f *fakeSender) Send(_ context.Context, to, code string, _ int32, ttl time.Duration) error {
	f.lastTo, f.lastCode, f.lastTTL = to, code, ttl
	f.sentCount++
	return f.err
}

type fakeIDs struct {
	mu  sync.Mutex
	n   int64
	err error
}

func (f *fakeIDs) NextID() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	f.n++
	return 1000 + f.n, nil
}

type fakeRevoker struct {
	mu       sync.Mutex
	sessions []int64
	users    map[int64]time.Time
}

func (f *fakeRevoker) RevokeSessions(_ context.Context, _ time.Duration, ids ...int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, ids...)
	return nil
}

func (f *fakeRevoker) RevokeUser(_ context.Context, id int64, at time.Time, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[id] = at
	return nil
}
