package biz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"sort"
	"sync"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	accountv1 "github.com/Mars-08042/MarsChat/api/account/v1"
	v1 "github.com/Mars-08042/MarsChat/api/message/v1"
	"github.com/Mars-08042/MarsChat/app/message/internal/conf"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"
)

type memberKey struct{ conv, user int64 }

type clientKey struct {
	conv, sender int64
	client       string
}

type event struct {
	topic, key string
	msg        proto.Message
}

// store 是内存中的“数据库”。事务开始时拍快照，fn 返回错误时恢复，模拟回滚。
type store struct {
	mu      sync.Mutex
	convs   map[int64]*Conversation
	members map[memberKey]*Member
	msgs    map[SeqKey]*Message
	events  []event
}

func newStore() *store {
	return &store{convs: map[int64]*Conversation{}, members: map[memberKey]*Member{}, msgs: map[SeqKey]*Message{}}
}

func (s *store) snapshot() *store {
	cp := &store{convs: map[int64]*Conversation{}, members: map[memberKey]*Member{}, msgs: maps.Clone(s.msgs),
		events: append([]event(nil), s.events...)}
	for k, v := range s.convs {
		c := *v
		cp.convs[k] = &c
	}
	for k, v := range s.members {
		m := *v
		cp.members[k] = &m
	}
	return cp
}

type fakeTx struct{ s *store }

func (t fakeTx) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	t.s.mu.Lock()
	snap := t.s.snapshot()
	t.s.mu.Unlock()
	if err := fn(ctx); err != nil {
		t.s.mu.Lock()
		t.s.convs, t.s.members, t.s.msgs, t.s.events = snap.convs, snap.members, snap.msgs, snap.events
		t.s.mu.Unlock()
		return err
	}
	return nil
}

type fakeConvs struct{ s *store }

func (r fakeConvs) Get(_ context.Context, id int64) (*Conversation, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if c, ok := r.s.convs[id]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, ErrNotFound
}

func (r fakeConvs) FindPair(_ context.Context, low, high int64) (*Conversation, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, c := range r.s.convs {
		if c.Type == TypeSingle && c.UserLow == low && c.UserHigh == high {
			cp := *c
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (r fakeConvs) BatchGet(ctx context.Context, ids []int64) (map[int64]*Conversation, error) {
	out := map[int64]*Conversation{}
	for _, id := range ids {
		if c, err := r.Get(ctx, id); err == nil {
			out[id] = c
		}
	}
	return out, nil
}

func (r fakeConvs) CreateIfAbsent(ctx context.Context, c *Conversation) error {
	if _, err := r.Get(ctx, c.ID); err == nil {
		return nil
	}
	if c.Type == TypeSingle {
		if _, err := r.FindPair(ctx, c.UserLow, c.UserHigh); err == nil {
			return nil
		}
	}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	cp := *c
	r.s.convs[c.ID] = &cp
	return nil
}

func (r fakeConvs) Lock(ctx context.Context, id int64) (*Conversation, error) { return r.Get(ctx, id) }

func (r fakeConvs) LockPair(ctx context.Context, low, high int64) (*Conversation, error) {
	return r.FindPair(ctx, low, high)
}

func (r fakeConvs) SetMaxSeq(_ context.Context, id, seq int64, now time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.convs[id].MaxSeq = seq
	r.s.convs[id].UpdatedAt = now
	return nil
}

type fakeMembers struct{ s *store }

func (r fakeMembers) Get(_ context.Context, conv, user int64) (*Member, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if m, ok := r.s.members[memberKey{conv, user}]; ok {
		cp := *m
		return &cp, nil
	}
	return nil, ErrNotFound
}

func (r fakeMembers) AddIgnore(_ context.Context, ms []*Member) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, m := range ms {
		k := memberKey{m.ConversationID, m.UserID}
		if _, ok := r.s.members[k]; !ok {
			cp := *m
			r.s.members[k] = &cp
		}
	}
	return nil
}

func (r fakeMembers) AddAtLatest(_ context.Context, conv int64, users []int64, now time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	maxSeq := r.s.convs[conv].MaxSeq
	for _, u := range users {
		k := memberKey{conv, u}
		if _, ok := r.s.members[k]; !ok {
			r.s.members[k] = &Member{ConversationID: conv, UserID: u, ReadSeq: maxSeq, JoinedAt: now, UpdatedAt: now}
		}
	}
	return nil
}

func (r fakeMembers) Remove(_ context.Context, conv, user int64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	delete(r.s.members, memberKey{conv, user})
	return nil
}

func (r fakeMembers) ListByUser(_ context.Context, user, cursor int64, limit int) ([]*Member, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []*Member
	for k, m := range r.s.members {
		if k.user == user && k.conv > cursor {
			cp := *m
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConversationID < out[j].ConversationID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r fakeMembers) ListOthers(_ context.Context, convs []int64, me int64) (map[int64]*Member, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := map[int64]*Member{}
	for _, c := range convs {
		for k, m := range r.s.members {
			if k.conv == c && k.user != me {
				cp := *m
				out[c] = &cp
			}
		}
	}
	return out, nil
}

func (r fakeMembers) AdvanceRead(_ context.Context, conv, user, seq int64, now time.Time) (bool, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	m, ok := r.s.members[memberKey{conv, user}]
	if !ok || m.ReadSeq >= seq {
		return false, nil
	}
	m.ReadSeq = seq
	m.UpdatedAt = now
	return true, nil
}

type fakeMsgs struct{ s *store }

func (r fakeMsgs) Create(_ context.Context, m *Message) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, e := range r.s.msgs {
		if e.ConversationID == m.ConversationID && e.SenderID == m.SenderID && e.ClientMsgID == m.ClientMsgID {
			return ErrDuplicate
		}
	}
	k := SeqKey{m.ConversationID, m.Seq}
	if _, ok := r.s.msgs[k]; ok {
		return errors.New("primary key conflict: seq allocated twice")
	}
	cp := *m
	r.s.msgs[k] = &cp
	return nil
}

func (r fakeMsgs) FindByClientID(_ context.Context, conv, sender int64, client string) (*Message, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for _, e := range r.s.msgs {
		if e.ConversationID == conv && e.SenderID == sender && e.ClientMsgID == client {
			cp := *e
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (r fakeMsgs) list(conv int64, keep func(seq int64) bool, desc bool, limit int) []*Message {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []*Message
	for k, m := range r.s.msgs {
		if k.ConversationID == conv && keep(k.Seq) {
			cp := *m
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if desc {
			return out[i].Seq > out[j].Seq
		}
		return out[i].Seq < out[j].Seq
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (r fakeMsgs) ListBefore(_ context.Context, conv, before int64, limit int) ([]*Message, error) {
	return r.list(conv, func(s int64) bool { return s < before }, true, limit), nil
}

func (r fakeMsgs) ListAfter(_ context.Context, conv, after int64, limit int) ([]*Message, error) {
	return r.list(conv, func(s int64) bool { return s > after }, false, limit), nil
}

func (r fakeMsgs) BatchGet(_ context.Context, keys []SeqKey) (map[SeqKey]*Message, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	out := map[SeqKey]*Message{}
	for _, k := range keys {
		if m, ok := r.s.msgs[k]; ok {
			cp := *m
			out[k] = &cp
		}
	}
	return out, nil
}

type fakeEvents struct{ s *store }

func (r fakeEvents) Add(_ context.Context, topic, key string, msg proto.Message, _ time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.events = append(r.s.events, event{topic: topic, key: key, msg: msg})
	return nil
}

// fakePerm 按 deny 返回错误；hook 在校验时执行，用来模拟校验期间另一个请求提交了同一条消息。
type fakePerm struct {
	calls int
	deny  error
	hook  func()
}

func (p *fakePerm) CheckSend(context.Context, int64, int32, int64) error {
	p.calls++
	if p.hook != nil {
		p.hook()
	}
	return p.deny
}

type fakeUsers struct{ err error }

func (u fakeUsers) BatchGetUsers(_ context.Context, ids []int64) (map[int64]*accountv1.UserProfile, error) {
	if u.err != nil {
		return nil, u.err
	}
	out := map[int64]*accountv1.UserProfile{}
	for _, id := range ids {
		out[id] = &accountv1.UserProfile{Id: id, Nickname: "u"}
	}
	return out, nil
}

type fakeGroups struct{ err error }

func (g fakeGroups) BatchGetGroups(_ context.Context, ids []int64) (map[int64]*GroupBrief, error) {
	if g.err != nil {
		return nil, g.err
	}
	out := map[int64]*GroupBrief{}
	for _, id := range ids {
		out[id] = &GroupBrief{ID: id, Name: "g"}
	}
	return out, nil
}

type fakeLimiter struct {
	deny bool
	err  error
}

func (l fakeLimiter) Allow(context.Context, string, int, time.Duration) (ratelimit.Result, error) {
	if l.err != nil {
		return ratelimit.Result{}, l.err
	}
	if l.deny {
		return ratelimit.Result{Allowed: false, RetryAfter: 1500 * time.Millisecond}, nil
	}
	return ratelimit.Result{Allowed: true}, nil
}

type fakeIDs struct{ next int64 }

func (g *fakeIDs) NextID() (int64, error) {
	g.next++
	return 1_000_000 + g.next, nil
}

type env struct {
	s       *store
	perm    *fakePerm
	limiter *fakeLimiter
	users   *fakeUsers
	groups  *fakeGroups
	policy  *PolicyHolder
	msg     *MessageUsecase
	conv    *ConversationUsecase
	members *MembershipUsecase
}

func testPolicy() *conf.Policy {
	return &conf.Policy{
		SendMessage:        &pkgconf.RateLimit{Limit: 30, Window: durationpb.New(10 * time.Second)},
		DifferenceMaxLimit: 200,
		DifferenceTooLong:  1000,
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := newStore()
	e := &env{s: s, perm: &fakePerm{}, limiter: &fakeLimiter{}, users: &fakeUsers{}, groups: &fakeGroups{},
		policy: NewPolicyHolder(testPolicy())}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tx, convs, members, msgs, events := fakeTx{s}, fakeConvs{s}, fakeMembers{s}, fakeMsgs{s}, fakeEvents{s}
	e.msg = NewMessageUsecase(tx, convs, members, msgs, events, e.perm, limiterAdapter{e.limiter}, &fakeIDs{}, e.policy, logger)
	e.conv = NewConversationUsecase(tx, convs, members, msgs, events, usersAdapter{e.users}, groupsAdapter{e.groups}, e.policy, logger)
	e.members = NewMembershipUsecase(tx, convs, members)
	return e
}

// 适配器让测试在创建用例之后还能修改假依赖的行为。
type limiterAdapter struct{ l *fakeLimiter }

func (a limiterAdapter) Allow(ctx context.Context, k string, n int, w time.Duration) (ratelimit.Result, error) {
	return a.l.Allow(ctx, k, n, w)
}

type usersAdapter struct{ u *fakeUsers }

func (a usersAdapter) BatchGetUsers(ctx context.Context, ids []int64) (map[int64]*accountv1.UserProfile, error) {
	return a.u.BatchGetUsers(ctx, ids)
}

type groupsAdapter struct{ g *fakeGroups }

func (a groupsAdapter) BatchGetGroups(ctx context.Context, ids []int64) (map[int64]*GroupBrief, error) {
	return a.g.BatchGetGroups(ctx, ids)
}

func text(s string) *v1.MessageContent {
	return &v1.MessageContent{Body: &v1.MessageContent_Text{Text: &v1.TextContent{Text: s}}}
}

func (e *env) send(t *testing.T, sender int64, peerType int32, peer int64, client string) *Message {
	t.Helper()
	m, err := e.msg.Send(context.Background(), SendInput{
		SenderID: sender, ConnectionID: 77, PeerType: peerType, PeerID: peer, ClientMsgID: client, Content: text(client),
	})
	if err != nil {
		t.Fatalf("send %s: %v", client, err)
	}
	return m
}

func (e *env) eventCount() int {
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	return len(e.s.events)
}

func (e *env) lastEvent(t *testing.T) proto.Message {
	t.Helper()
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	if len(e.s.events) == 0 {
		t.Fatal("no events")
	}
	return e.s.events[len(e.s.events)-1].msg
}

func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	if got := kerrors.Reason(err); got != reason {
		t.Fatalf("reason = %q (err %v), want %s", got, err, reason)
	}
}
