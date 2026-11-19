package biz

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/proto"

	"github.com/Mars-08042/MarsChat/app/social/internal/conf"
	pkgconf "github.com/Mars-08042/MarsChat/pkg/conf"
	"github.com/Mars-08042/MarsChat/pkg/ratelimit"

	"google.golang.org/protobuf/types/known/durationpb"
)

// 内存实现的存储，只模拟 biz 依赖的语义（条件更新、唯一约束等），不模拟事务回滚。

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeTx struct{}

func (fakeTx) InTx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type pair [2]int64

type fakeFriends struct {
	mu   sync.Mutex
	rows map[pair]time.Time
}

func (f *fakeFriends) AreFriends(_ context.Context, userID, friendID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[pair{userID, friendID}]
	return ok, nil
}

func (f *fakeFriends) AddPair(_ context.Context, a, b int64, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[pair{a, b}]; ok {
		return false, nil
	}
	f.rows[pair{a, b}], f.rows[pair{b, a}] = now, now
	return true, nil
}

func (f *fakeFriends) DeletePair(_ context.Context, a, b int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[pair{a, b}]
	delete(f.rows, pair{a, b})
	delete(f.rows, pair{b, a})
	return ok, nil
}

func (f *fakeFriends) FilterFriends(_ context.Context, userID int64, ids []int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for _, id := range ids {
		if _, ok := f.rows[pair{userID, id}]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeFriends) List(_ context.Context, userID, after int64, limit int) ([]*Friend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Friend
	for k, t := range f.rows {
		if k[0] == userID && k[1] > after {
			out = append(out, &Friend{UserID: k[0], FriendID: k[1], CreatedAt: t})
		}
	}
	slices.SortFunc(out, func(a, b *Friend) int { return cmp.Compare(a.FriendID, b.FriendID) })
	return out[:min(limit, len(out))], nil
}

type fakeRequests struct {
	mu   sync.Mutex
	byID map[int64]*FriendRequest
	// beforeCreate 在 Create 检查唯一约束之前调用，用来模拟并发插入
	beforeCreate func()
}

func (f *fakeRequests) put(r *FriendRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *r
	f.byID[r.ID] = &cp
}

func (f *fakeRequests) Create(_ context.Context, r *FriendRequest) error {
	if hook := f.beforeCreate; hook != nil {
		f.beforeCreate = nil
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.byID {
		if x.FromUserID == r.FromUserID && x.ToUserID == r.ToUserID && x.Status == RequestPending {
			return ErrDuplicate
		}
	}
	cp := *r
	f.byID[r.ID] = &cp
	return nil
}

func (f *fakeRequests) FindByID(_ context.Context, id int64) (*FriendRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *fakeRequests) FindPending(_ context.Context, from, to int64) (*FriendRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.byID {
		if r.FromUserID == from && r.ToUserID == to && r.Status == RequestPending {
			cp := *r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeRequests) Refresh(_ context.Context, id int64, message string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.byID[id]; ok && r.Status == RequestPending {
		r.Message, r.UpdatedAt = message, now
	}
	return nil
}

func (f *fakeRequests) Handle(_ context.Context, id, toUserID int64, status int32, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok || r.ToUserID != toUserID || r.Status != RequestPending {
		return false, nil
	}
	r.Status, r.HandledAt, r.UpdatedAt = status, &now, now
	return true, nil
}

func (f *fakeRequests) ResolvePending(_ context.Context, from, to int64, status int32, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.byID {
		if r.FromUserID == from && r.ToUserID == to && r.Status == RequestPending {
			r.Status, r.HandledAt, r.UpdatedAt = status, &now, now
		}
	}
	return nil
}

func (f *fakeRequests) list(match func(*FriendRequest) bool, before int64, limit int) []*FriendRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*FriendRequest
	for _, r := range f.byID {
		if match(r) && (before == 0 || r.ID < before) {
			cp := *r
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *FriendRequest) int { return cmp.Compare(b.ID, a.ID) })
	return out[:min(limit, len(out))]
}

func (f *fakeRequests) ListReceived(_ context.Context, toUserID, before int64, limit int) ([]*FriendRequest, error) {
	return f.list(func(r *FriendRequest) bool { return r.ToUserID == toUserID && r.Status == RequestPending }, before, limit), nil
}

func (f *fakeRequests) ListSent(_ context.Context, fromUserID, before int64, limit int) ([]*FriendRequest, error) {
	return f.list(func(r *FriendRequest) bool { return r.FromUserID == fromUserID }, before, limit), nil
}

type fakeBlocks struct {
	mu   sync.Mutex
	rows map[pair]time.Time
}

func (f *fakeBlocks) Add(_ context.Context, userID, blockedID int64, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[pair{userID, blockedID}]; !ok {
		f.rows[pair{userID, blockedID}] = now
	}
	return nil
}

func (f *fakeBlocks) Remove(_ context.Context, userID, blockedID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, pair{userID, blockedID})
	return nil
}

func (f *fakeBlocks) EitherBlocked(_ context.Context, a, b int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ab := f.rows[pair{a, b}]
	_, ba := f.rows[pair{b, a}]
	return ab || ba, nil
}

func (f *fakeBlocks) List(_ context.Context, userID, after int64, limit int) ([]*Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Block
	for k, t := range f.rows {
		if k[0] == userID && k[1] > after {
			out = append(out, &Block{UserID: k[0], BlockedID: k[1], CreatedAt: t})
		}
	}
	slices.SortFunc(out, func(a, b *Block) int { return cmp.Compare(a.BlockedID, b.BlockedID) })
	return out[:min(limit, len(out))], nil
}

type fakeGroups struct {
	mu      sync.Mutex
	byID    map[int64]*Group
	evicted []int64
	created []int64
}

func (f *fakeGroups) get(id int64) (*Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *g
	return &cp, nil
}

func (f *fakeGroups) Create(_ context.Context, g *Group) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *g
	f.byID[g.ID] = &cp
	return nil
}

func (f *fakeGroups) FindByID(_ context.Context, id int64) (*Group, error) { return f.get(id) }
func (f *fakeGroups) Lock(_ context.Context, id int64) (*Group, error)     { return f.get(id) }
func (f *fakeGroups) GetCached(_ context.Context, id int64) (*Group, error) {
	return f.get(id)
}

func (f *fakeGroups) UpdateProfile(_ context.Context, id int64, version int32, upd GroupUpdate, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.byID[id]
	if !ok || g.Version != version || g.Status != GroupNormal {
		return false, nil
	}
	if upd.Name != nil {
		g.Name = *upd.Name
	}
	if upd.Avatar != nil {
		g.Avatar = *upd.Avatar
	}
	if upd.Notice != nil {
		g.Notice = *upd.Notice
	}
	if upd.JoinMode != nil {
		g.JoinMode = *upd.JoinMode
	}
	if upd.MuteAll != nil {
		g.MuteAll = *upd.MuteAll
	}
	g.Version++
	g.UpdatedAt = now
	return true, nil
}

func (f *fakeGroups) AddMemberCount(_ context.Context, id int64, delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].MemberCount += int32(delta)
	return nil
}

func (f *fakeGroups) SetOwner(_ context.Context, id, ownerID int64, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.byID[id]
	g.OwnerID, g.UpdatedAt = ownerID, now
	g.Version++
	return nil
}

func (f *fakeGroups) Dismiss(_ context.Context, id int64, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.byID[id]
	g.Status, g.MemberCount, g.UpdatedAt, g.DismissedAt = GroupDismissed, 0, now, &now
	g.Version++
	return nil
}

func (f *fakeGroups) BatchGetCached(_ context.Context, ids []int64) ([]*Group, error) {
	var out []*Group
	for _, id := range ids {
		if g, err := f.get(id); err == nil {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeGroups) EvictCache(_ context.Context, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, id)
}

func (f *fakeGroups) OnCreated(_ context.Context, id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, id)
}

type fakeMembers struct {
	mu      sync.Mutex
	byGroup map[int64]map[int64]*Member
	evicted []int64
}

func (f *fakeMembers) put(m *Member) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byGroup[m.GroupID] == nil {
		f.byGroup[m.GroupID] = map[int64]*Member{}
	}
	cp := *m
	f.byGroup[m.GroupID][m.UserID] = &cp
}

func (f *fakeMembers) Find(_ context.Context, groupID, userID int64) (*Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.byGroup[groupID][userID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (f *fakeMembers) FilterMembers(_ context.Context, groupID int64, userIDs []int64) ([]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for _, id := range userIDs {
		if _, ok := f.byGroup[groupID][id]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeMembers) Add(_ context.Context, members []*Member) error {
	f.mu.Lock()
	for _, m := range members {
		if _, ok := f.byGroup[m.GroupID][m.UserID]; ok {
			f.mu.Unlock()
			return errors.New("duplicate member")
		}
	}
	f.mu.Unlock()
	for _, m := range members {
		f.put(m)
	}
	return nil
}

func (f *fakeMembers) Remove(_ context.Context, groupID, userID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.byGroup[groupID][userID]
	delete(f.byGroup[groupID], userID)
	return ok, nil
}

func (f *fakeMembers) SetRole(_ context.Context, groupID, userID int64, role int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byGroup[groupID][userID].Role = role
	return nil
}

func (f *fakeMembers) SetMutedUntil(_ context.Context, groupID, userID int64, until *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byGroup[groupID][userID].MutedUntil = until
	return nil
}

func (f *fakeMembers) all(groupID int64) []*Member {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Member
	for _, m := range f.byGroup[groupID] {
		cp := *m
		out = append(out, &cp)
	}
	slices.SortFunc(out, func(a, b *Member) int { return cmp.Compare(a.UserID, b.UserID) })
	return out
}

func (f *fakeMembers) RemoveAll(_ context.Context, groupID int64) ([]int64, error) {
	var ids []int64
	for _, m := range f.all(groupID) {
		ids = append(ids, m.UserID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byGroup, groupID)
	return ids, nil
}

func (f *fakeMembers) List(_ context.Context, groupID, after int64, limit int) ([]*Member, error) {
	var out []*Member
	for _, m := range f.all(groupID) {
		if m.UserID > after {
			out = append(out, m)
		}
	}
	return out[:min(limit, len(out))], nil
}

func (f *fakeMembers) ListByUser(_ context.Context, userID, after int64, limit int) ([]*Member, error) {
	f.mu.Lock()
	var out []*Member
	for gid, ms := range f.byGroup {
		if m, ok := ms[userID]; ok && gid > after {
			cp := *m
			out = append(out, &cp)
		}
	}
	f.mu.Unlock()
	slices.SortFunc(out, func(a, b *Member) int { return cmp.Compare(a.GroupID, b.GroupID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeMembers) GetAllCached(_ context.Context, groupID int64) ([]*Member, error) {
	return f.all(groupID), nil
}

func (f *fakeMembers) EvictCache(_ context.Context, groupID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, groupID)
}

type fakeJoinRequests struct {
	mu   sync.Mutex
	byID map[int64]*JoinRequest
}

func (f *fakeJoinRequests) Create(_ context.Context, r *JoinRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.byID {
		if x.GroupID == r.GroupID && x.UserID == r.UserID && x.Status == RequestPending {
			return ErrDuplicate
		}
	}
	cp := *r
	f.byID[r.ID] = &cp
	return nil
}

func (f *fakeJoinRequests) FindByID(_ context.Context, id int64) (*JoinRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (f *fakeJoinRequests) FindPending(_ context.Context, groupID, userID int64) (*JoinRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.byID {
		if r.GroupID == groupID && r.UserID == userID && r.Status == RequestPending {
			cp := *r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeJoinRequests) Refresh(_ context.Context, id int64, message string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.byID[id]; ok && r.Status == RequestPending {
		r.Message, r.UpdatedAt = message, now
	}
	return nil
}

func (f *fakeJoinRequests) Handle(_ context.Context, id int64, status int32, handledBy int64, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok || r.Status != RequestPending {
		return false, nil
	}
	r.Status, r.HandledBy, r.HandledAt, r.UpdatedAt = status, handledBy, &now, now
	return true, nil
}

func (f *fakeJoinRequests) ExpireForUsers(_ context.Context, groupID int64, userIDs []int64, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.byID {
		if r.GroupID == groupID && r.Status == RequestPending && slices.Contains(userIDs, r.UserID) {
			r.Status, r.HandledAt, r.UpdatedAt = RequestExpired, &now, now
		}
	}
	return nil
}

func (f *fakeJoinRequests) ExpireAll(_ context.Context, groupID int64, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.byID {
		if r.GroupID == groupID && r.Status == RequestPending {
			r.Status, r.HandledAt, r.UpdatedAt = RequestExpired, &now, now
		}
	}
	return nil
}

func (f *fakeJoinRequests) ListPending(_ context.Context, groupID, before int64, limit int) ([]*JoinRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*JoinRequest
	for _, r := range f.byID {
		if r.GroupID == groupID && r.Status == RequestPending && (before == 0 || r.ID < before) {
			cp := *r
			out = append(out, &cp)
		}
	}
	slices.SortFunc(out, func(a, b *JoinRequest) int { return cmp.Compare(b.ID, a.ID) })
	return out[:min(limit, len(out))], nil
}

type event struct {
	key string
	msg proto.Message
}

type fakeEvents struct {
	mu     sync.Mutex
	events []event
}

func (f *fakeEvents) Add(_ context.Context, topic, key string, msg proto.Message, _ time.Time) error {
	if topic != TopicSocialEvents {
		return errors.New("unexpected topic " + topic)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event{key: key, msg: msg})
	return nil
}

// take 返回并清空已记录的事件。
func (f *fakeEvents) take() []event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.events
	f.events = nil
	return out
}

type fakeUsers struct {
	mu    sync.Mutex
	users map[int64]*UserProfile
	err   error
}

func (f *fakeUsers) BatchGetUsers(_ context.Context, ids []int64) (map[int64]*UserProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[int64]*UserProfile{}
	for _, id := range ids {
		if u, ok := f.users[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

type fakeLimiter struct {
	mu      sync.Mutex
	deny    bool
	err     error
	allowed map[string]int
}

func (f *fakeLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (ratelimit.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return ratelimit.Result{}, f.err
	}
	if f.deny {
		return ratelimit.Result{Allowed: false, RetryAfter: 90 * time.Second}, nil
	}
	f.allowed[key]++
	return ratelimit.Result{Allowed: true}, nil
}

type fakeIDs struct {
	mu   sync.Mutex
	next int64
}

func (f *fakeIDs) NextID() (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	return f.next, nil
}

// 测试用户：1～9 正常，99 已封禁，其他 ID 不存在。
const bannedUser int64 = 99

type env struct {
	friends  *fakeFriends
	requests *fakeRequests
	blocks   *fakeBlocks
	groups   *fakeGroups
	members  *fakeMembers
	joins    *fakeJoinRequests
	events   *fakeEvents
	users    *fakeUsers
	limiter  *fakeLimiter
	policy   *PolicyHolder
	now      time.Time

	friendUC *FriendUsecase
	groupUC  *GroupUsecase
	permUC   *PermissionUsecase
}

func testPolicy() *conf.Policy {
	rl := func(n int32) *pkgconf.RateLimit {
		return &pkgconf.RateLimit{Limit: n, Window: durationpb.New(time.Hour)}
	}
	return &conf.Policy{
		FriendRequest: rl(20), GroupCreate: rl(10), JoinRequest: rl(20),
		MaxGroupMembers: 500, MaxInitialMembers: 50,
	}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		friends:  &fakeFriends{rows: map[pair]time.Time{}},
		requests: &fakeRequests{byID: map[int64]*FriendRequest{}},
		blocks:   &fakeBlocks{rows: map[pair]time.Time{}},
		groups:   &fakeGroups{byID: map[int64]*Group{}},
		members:  &fakeMembers{byGroup: map[int64]map[int64]*Member{}},
		joins:    &fakeJoinRequests{byID: map[int64]*JoinRequest{}},
		events:   &fakeEvents{},
		users:    &fakeUsers{users: map[int64]*UserProfile{}},
		limiter:  &fakeLimiter{allowed: map[string]int{}},
		policy:   NewPolicyHolder(testPolicy()),
		now:      time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC),
	}
	for id := int64(1); id <= 9; id++ {
		e.users.users[id] = &UserProfile{ID: id, Nickname: "u", Status: 1}
	}
	e.users.users[bannedUser] = &UserProfile{ID: bannedUser, Status: userStatusBanned}

	ids := &fakeIDs{next: 1000}
	clock := func() time.Time { return e.now }
	e.friendUC = NewFriendUsecase(fakeTx{}, e.friends, e.requests, e.blocks, e.events, e.users, ids, e.limiter, e.policy, discard)
	e.friendUC.now = clock
	e.groupUC = NewGroupUsecase(fakeTx{}, e.groups, e.members, e.joins, e.friends, e.events, ids, e.limiter, e.policy, discard)
	e.groupUC.now = clock
	e.permUC = NewPermissionUsecase(e.friends, e.blocks, e.groupUC, e.members)
	e.permUC.now = clock
	return e
}

// befriend 直接写入好友关系，绕过申请流程。
func (e *env) befriend(a, b int64) {
	_, _ = e.friends.AddPair(context.Background(), a, b, e.now)
}

// newGroup 直接写入一个群：owner 为群主，admins 为管理员，members 为普通成员。
func (e *env) newGroup(id, owner int64, joinMode int32, admins, members []int64) {
	ctx := context.Background()
	_ = e.groups.Create(ctx, &Group{
		ID: id, Name: "g", OwnerID: owner, JoinMode: joinMode, Status: GroupNormal, Version: 1,
		MemberCount: int32(1 + len(admins) + len(members)), CreatedAt: e.now, UpdatedAt: e.now,
	})
	e.members.put(&Member{GroupID: id, UserID: owner, Role: RoleOwner, JoinedAt: e.now})
	for _, uid := range admins {
		e.members.put(&Member{GroupID: id, UserID: uid, Role: RoleAdmin, JoinedAt: e.now})
	}
	for _, uid := range members {
		e.members.put(&Member{GroupID: id, UserID: uid, Role: RoleMember, JoinedAt: e.now})
	}
}

func (e *env) group(t *testing.T, id int64) *Group {
	t.Helper()
	g, err := e.groups.get(id)
	if err != nil {
		t.Fatalf("group %d: %v", id, err)
	}
	return g
}

func (e *env) role(groupID, userID int64) int32 {
	m, err := e.members.Find(context.Background(), groupID, userID)
	if err != nil {
		return 0
	}
	return m.Role
}

// wantReason 断言 err 是指定 reason 的 Kratos 错误；reason 为空表示期望成功。
func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	if reason == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if got := kerrors.Reason(err); got != reason {
		t.Fatalf("reason = %q (err %v), want %q", got, err, reason)
	}
}

// eventTypes 返回事件的消息类型名，便于断言。
func eventTypes(events []event) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = string(ev.msg.ProtoReflect().Descriptor().Name())
	}
	return out
}

// keysOf 返回 map 的键，按升序排列。
func keysOf[V any](m map[int64]V) []int64 {
	return slices.Sorted(maps.Keys(m))
}
