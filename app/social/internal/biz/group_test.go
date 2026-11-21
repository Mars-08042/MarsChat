package biz

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
	"github.com/Mars-08042/MarsChat/pkg/idgen"
)

// 节点号租约失效时，需要发号的写操作返回 503，什么都不写。
func TestIDLeaseLostReturnsUnavailable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(500, 1, JoinModeApproval, nil, nil)
	e.ids.err = idgen.ErrLeaseLost

	_, err := e.groupUC.Create(ctx, 1, CreateGroupInput{Name: "g", JoinMode: JoinModeFree})
	wantReason(t, err, v1.ErrorReason_DEPENDENCY_UNAVAILABLE.String())
	_, err = e.friendUC.SendRequest(ctx, 1, 2, "hi")
	wantReason(t, err, v1.ErrorReason_DEPENDENCY_UNAVAILABLE.String())
	_, err = e.groupUC.Join(ctx, 2, 500, "hi")
	wantReason(t, err, v1.ErrorReason_DEPENDENCY_UNAVAILABLE.String())
	if len(e.groups.byID) != 1 || len(e.requests.byID) != 0 || len(e.joins.byID) != 0 || len(e.events.take()) != 0 {
		t.Fatal("failed writes must not store anything or emit events")
	}

	e.ids.err = nil
	if _, err := e.friendUC.SendRequest(ctx, 1, 2, "hi"); err != nil {
		t.Fatalf("send request after the lease recovered: %v", err)
	}
}

// 建群：初始成员必须是好友，数量受策略限制。
func TestCreateGroup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.befriend(1, 2)
	e.befriend(1, 3)

	_, err := e.groupUC.Create(ctx, 1, CreateGroupInput{Name: "  ", JoinMode: JoinModeFree})
	wantReason(t, err, "VALIDATOR")
	_, err = e.groupUC.Create(ctx, 1, CreateGroupInput{Name: "g", JoinMode: JoinModeFree, MemberIDs: []int64{2, 4}})
	wantReason(t, err, "NOT_FRIENDS")
	p := testPolicy()
	p.MaxInitialMembers = 1
	e.policy.Store(p)
	_, err = e.groupUC.Create(ctx, 1, CreateGroupInput{Name: "g", JoinMode: JoinModeFree, MemberIDs: []int64{2, 3}})
	wantReason(t, err, "VALIDATOR")
	e.policy.Store(testPolicy())
	if len(e.groups.byID) != 0 || len(e.events.take()) != 0 {
		t.Fatal("rejected create must not store groups or emit events")
	}

	// 创建者自己出现在初始成员里时忽略，名称去掉首尾空白
	g, err := e.groupUC.Create(ctx, 1, CreateGroupInput{Name: " team ", JoinMode: JoinModeApproval, MemberIDs: []int64{2, 3, 1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "team" || g.MemberCount != 3 || g.Version != 1 || g.OwnerID != 1 {
		t.Fatalf("group = %+v", g)
	}
	if e.role(g.ID, 1) != RoleOwner || e.role(g.ID, 2) != RoleMember || e.role(g.ID, 3) != RoleMember {
		t.Fatal("members not stored with expected roles")
	}
	if m, _ := e.members.Find(ctx, g.ID, 2); m.InviterID != 1 {
		t.Fatalf("inviter = %d, want owner", m.InviterID)
	}
	if !slices.Equal(e.groups.created, []int64{g.ID}) {
		t.Fatal("group id not added to bloom filter")
	}
	evs := e.events.take()
	ev, ok := evs[0].msg.(*v1.GroupCreated)
	if len(evs) != 1 || !ok || !slices.Equal(ev.GetMemberIds(), []int64{1, 2, 3}) {
		t.Fatalf("events = %v", eventTypes(evs))
	}
}

// 直接加入的群立即入群；已是成员时报错；满员时报错。
func TestJoinFreeGroup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, nil)

	res, err := e.groupUC.Join(ctx, 2, 100, "")
	if err != nil || !res.Joined || res.Request != nil {
		t.Fatalf("Join = %+v, %v", res, err)
	}
	if e.group(t, 100).MemberCount != 2 || e.role(100, 2) != RoleMember {
		t.Fatal("member not added or count not updated")
	}
	if got := eventTypes(e.events.take()); !slices.Equal(got, []string{"GroupMembersJoined"}) {
		t.Fatalf("events = %v", got)
	}
	if !slices.Contains(e.members.evicted, 100) || !slices.Contains(e.groups.evicted, 100) {
		t.Fatal("caches not evicted after join")
	}
	_, err = e.groupUC.Join(ctx, 2, 100, "")
	wantReason(t, err, "ALREADY_GROUP_MEMBER")

	p := testPolicy()
	p.MaxGroupMembers = 2
	e.policy.Store(p)
	_, err = e.groupUC.Join(ctx, 3, 100, "")
	wantReason(t, err, "GROUP_FULL")

	_, err = e.groupUC.Join(ctx, 3, 404, "")
	wantReason(t, err, "GROUP_NOT_FOUND")
}

// 需审核的群：生成申请，重复申请只更新附言；管理员同意后入群，群成员不能审批。
func TestJoinApprovalGroup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeApproval, []int64{2}, []int64{3})

	res, err := e.groupUC.Join(ctx, 4, 100, "let me in")
	if err != nil || res.Joined || res.Request == nil {
		t.Fatalf("Join = %+v, %v", res, err)
	}
	if got := eventTypes(e.events.take()); !slices.Equal(got, []string{"GroupJoinRequestCreated"}) {
		t.Fatalf("events = %v", got)
	}
	again, _ := e.groupUC.Join(ctx, 4, 100, "please")
	if again.Request.ID != res.Request.ID || len(e.joins.byID) != 1 || len(e.events.take()) != 0 {
		t.Fatal("repeated join request should refresh the pending one without events")
	}

	list, _, err := e.groupUC.ListJoinRequests(ctx, 2, 100, 0, 0)
	if err != nil || len(list) != 1 || list[0].Message != "please" {
		t.Fatalf("ListJoinRequests = %+v, %v", list, err)
	}
	_, _, err = e.groupUC.ListJoinRequests(ctx, 3, 100, 0, 0)
	wantReason(t, err, "GROUP_PERMISSION_DENIED")
	_, _, err = e.groupUC.ListJoinRequests(ctx, 5, 100, 0, 0)
	wantReason(t, err, "NOT_GROUP_MEMBER")

	wantReason(t, e.groupUC.HandleJoinRequest(ctx, 3, res.Request.ID, true), "GROUP_PERMISSION_DENIED")
	wantReason(t, e.groupUC.HandleJoinRequest(ctx, 2, 404, true), "GROUP_JOIN_REQUEST_NOT_FOUND")
	wantReason(t, e.groupUC.HandleJoinRequest(ctx, 2, res.Request.ID, true), "")
	if e.role(100, 4) != RoleMember || e.group(t, 100).MemberCount != 4 {
		t.Fatal("applicant not added")
	}
	if r, _ := e.joins.FindByID(ctx, res.Request.ID); r.Status != RequestAccepted || r.HandledBy != 2 {
		t.Fatalf("request = %+v", r)
	}
	evs := e.events.take()
	if ev, ok := evs[0].msg.(*v1.GroupMembersJoined); len(evs) != 1 || !ok || ev.GetOperatorId() != 2 {
		t.Fatalf("events = %v", eventTypes(evs))
	}
	wantReason(t, e.groupUC.HandleJoinRequest(ctx, 1, res.Request.ID, false), "REQUEST_ALREADY_HANDLED")

	// 被拒后可以再申请
	r5, _ := e.groupUC.Join(ctx, 5, 100, "")
	wantReason(t, e.groupUC.HandleJoinRequest(ctx, 1, r5.Request.ID, false), "")
	r5b, err := e.groupUC.Join(ctx, 5, 100, "")
	if err != nil || r5b.Request.ID == r5.Request.ID {
		t.Fatalf("rejoin after reject = %+v, %v; want new request", r5b, err)
	}
}

// 同意申请时也要检查满员。
func TestAcceptJoinRequestWhenFull(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeApproval, nil, nil)
	res, _ := e.groupUC.Join(ctx, 2, 100, "")
	_ = e.members.Add(ctx, []*Member{{GroupID: 100, UserID: 3, Role: RoleMember}})
	_ = e.groups.AddMemberCount(ctx, 100, 1)
	p := testPolicy()
	p.MaxGroupMembers = 2
	e.policy.Store(p)
	wantReason(t, e.groupUC.HandleJoinRequest(ctx, 1, res.Request.ID, true), "GROUP_FULL")
}

// 修改资料：群主、管理员可以改，版本号不一致时冲突。
func TestUpdateGroupOptimisticLock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, []int64{2}, []int64{3})
	name := "new name"

	_, _, err := e.groupUC.Update(ctx, 3, 100, 1, GroupUpdate{Name: &name})
	wantReason(t, err, "GROUP_PERMISSION_DENIED")
	_, _, err = e.groupUC.Update(ctx, 4, 100, 1, GroupUpdate{Name: &name})
	wantReason(t, err, "NOT_GROUP_MEMBER")

	g, role, err := e.groupUC.Update(ctx, 2, 100, 1, GroupUpdate{Name: &name})
	if err != nil || g.Name != name || g.Version != 2 || role != RoleAdmin {
		t.Fatalf("Update = %+v, role %d, %v", g, role, err)
	}
	evs := e.events.take()
	if ev, ok := evs[0].msg.(*v1.GroupUpdated); len(evs) != 1 || !ok || ev.GetGroup().GetVersion() != 2 {
		t.Fatalf("events = %v", eventTypes(evs))
	}
	if !slices.Contains(e.groups.evicted, 100) {
		t.Fatal("group cache not evicted")
	}

	// 另一个管理员拿着旧版本号提交
	notice := "notice"
	_, _, err = e.groupUC.Update(ctx, 1, 100, 1, GroupUpdate{Notice: &notice})
	wantReason(t, err, "GROUP_VERSION_CONFLICT")
	if e.group(t, 100).Notice != "" {
		t.Fatal("conflicting update was applied")
	}
	// 空修改只校验版本号
	_, _, err = e.groupUC.Update(ctx, 1, 100, 1, GroupUpdate{})
	wantReason(t, err, "GROUP_VERSION_CONFLICT")
	g, _, err = e.groupUC.Update(ctx, 1, 100, 2, GroupUpdate{})
	if err != nil || g.Version != 2 || len(e.events.take()) != 0 {
		t.Fatalf("empty update = %+v, %v", g, err)
	}
}

// 权限矩阵（设计 3.4）：移除与禁言、设置管理员、转让、解散。
func TestGroupPermissionMatrix(t *testing.T) {
	const owner, admin, admin2, member, member2, outsider = 1, 2, 3, 4, 5, 6
	ops := []struct {
		name string
		do   func(e *env, actor, target int64) error
	}{
		{"remove", func(e *env, a, t int64) error { return e.groupUC.RemoveMember(context.Background(), a, 100, t) }},
		{"mute", func(e *env, a, t int64) error {
			_, err := e.groupUC.Mute(context.Background(), a, 100, t, time.Hour)
			return err
		}},
		{"set admin", func(e *env, a, t int64) error { return e.groupUC.SetRole(context.Background(), a, 100, t, RoleAdmin) }},
		{"transfer", func(e *env, a, t int64) error { return e.groupUC.Transfer(context.Background(), a, 100, t) }},
	}
	tests := []struct {
		actor, target int64
		want          map[string]string // 操作名 → 期望的 reason，缺省表示成功
	}{
		{actor: owner, target: admin, want: map[string]string{}},
		{actor: owner, target: member, want: map[string]string{}},
		{actor: admin, target: member, want: map[string]string{"set admin": "GROUP_PERMISSION_DENIED", "transfer": "GROUP_PERMISSION_DENIED"}},
		{actor: admin, target: admin2, want: map[string]string{"remove": "GROUP_PERMISSION_DENIED", "mute": "GROUP_PERMISSION_DENIED", "set admin": "GROUP_PERMISSION_DENIED", "transfer": "GROUP_PERMISSION_DENIED"}},
		{actor: admin, target: owner, want: map[string]string{"remove": "GROUP_PERMISSION_DENIED", "mute": "GROUP_PERMISSION_DENIED", "set admin": "GROUP_PERMISSION_DENIED", "transfer": "GROUP_PERMISSION_DENIED"}},
		{actor: member, target: member2, want: map[string]string{"remove": "GROUP_PERMISSION_DENIED", "mute": "GROUP_PERMISSION_DENIED", "set admin": "GROUP_PERMISSION_DENIED", "transfer": "GROUP_PERMISSION_DENIED"}},
		{actor: outsider, target: member, want: map[string]string{"remove": "NOT_GROUP_MEMBER", "mute": "NOT_GROUP_MEMBER", "set admin": "NOT_GROUP_MEMBER", "transfer": "NOT_GROUP_MEMBER"}},
		{actor: owner, target: outsider, want: map[string]string{"remove": "GROUP_MEMBER_NOT_FOUND", "mute": "GROUP_MEMBER_NOT_FOUND", "set admin": "GROUP_MEMBER_NOT_FOUND", "transfer": "GROUP_MEMBER_NOT_FOUND"}},
		{actor: owner, target: owner, want: map[string]string{"remove": "CANNOT_TARGET_SELF", "mute": "CANNOT_TARGET_SELF", "set admin": "CANNOT_TARGET_SELF", "transfer": "CANNOT_TARGET_SELF"}},
	}
	for _, tt := range tests {
		for _, op := range ops {
			t.Run(fmt.Sprintf("%s/actor%d/target%d", op.name, tt.actor, tt.target), func(t *testing.T) {
				e := newEnv(t)
				e.newGroup(100, owner, JoinModeFree, []int64{admin, admin2}, []int64{member, member2})
				wantReason(t, op.do(e, tt.actor, tt.target), tt.want[op.name])
			})
		}
	}
}

// 移除成员更新成员数、写事件、删除缓存。
func TestRemoveMember(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, []int64{2})
	wantReason(t, e.groupUC.RemoveMember(ctx, 1, 100, 2), "")
	if e.role(100, 2) != 0 || e.group(t, 100).MemberCount != 1 {
		t.Fatal("member not removed or count not updated")
	}
	evs := e.events.take()
	if ev, ok := evs[0].msg.(*v1.GroupMemberLeft); len(evs) != 1 || !ok || ev.GetReason() != v1.LeaveReason_LEAVE_REASON_REMOVED {
		t.Fatalf("events = %v", eventTypes(evs))
	}
	if !slices.Contains(e.members.evicted, 100) {
		t.Fatal("member cache not evicted")
	}
}

// 禁言与解除禁言。
func TestMuteMember(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, []int64{2})
	until, err := e.groupUC.Mute(ctx, 1, 100, 2, time.Hour)
	if err != nil || until == nil || !until.Equal(e.now.Add(time.Hour)) {
		t.Fatalf("Mute = %v, %v", until, err)
	}
	if m, _ := e.members.Find(ctx, 100, 2); !m.MutedAt(e.now) {
		t.Fatal("member not muted")
	}
	until, err = e.groupUC.Mute(ctx, 1, 100, 2, 0)
	if err != nil || until != nil {
		t.Fatalf("unmute = %v, %v", until, err)
	}
	if m, _ := e.members.Find(ctx, 100, 2); m.MutedUntil != nil {
		t.Fatal("member still muted")
	}
	if got := eventTypes(e.events.take()); !slices.Equal(got, []string{"GroupMemberUpdated", "GroupMemberUpdated"}) {
		t.Fatalf("events = %v", got)
	}
}

// 设置管理员：重复设置不写事件。
func TestSetRoleIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, []int64{2})
	wantReason(t, e.groupUC.SetRole(ctx, 1, 100, 2, RoleAdmin), "")
	wantReason(t, e.groupUC.SetRole(ctx, 1, 100, 2, RoleAdmin), "")
	if e.role(100, 2) != RoleAdmin || len(e.events.take()) != 1 {
		t.Fatal("role not set or duplicate event")
	}
	wantReason(t, e.groupUC.SetRole(ctx, 1, 100, 2, RoleMember), "")
	if e.role(100, 2) != RoleMember {
		t.Fatal("admin not revoked")
	}
	wantReason(t, e.groupUC.SetRole(ctx, 1, 100, 2, RoleOwner), "VALIDATOR")
}

// 群主不能直接退群；转让后原群主变为管理员，可以退群。
func TestTransferAndLeave(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, []int64{2, 3})
	muted := e.now.Add(time.Hour)
	_ = e.members.SetMutedUntil(ctx, 100, 2, &muted)

	wantReason(t, e.groupUC.Leave(ctx, 1, 100), "OWNER_CANNOT_LEAVE")
	wantReason(t, e.groupUC.Leave(ctx, 4, 100), "NOT_GROUP_MEMBER")
	wantReason(t, e.groupUC.Transfer(ctx, 1, 100, 2), "")
	g := e.group(t, 100)
	if g.OwnerID != 2 || g.Version != 2 || e.role(100, 2) != RoleOwner || e.role(100, 1) != RoleAdmin {
		t.Fatalf("after transfer: group %+v, roles %d/%d", g, e.role(100, 1), e.role(100, 2))
	}
	if m, _ := e.members.Find(ctx, 100, 2); m.MutedUntil != nil {
		t.Fatal("new owner still muted")
	}
	got := eventTypes(e.events.take())
	if !slices.Equal(got, []string{"GroupMemberUpdated", "GroupMemberUpdated", "GroupUpdated"}) {
		t.Fatalf("events = %v", got)
	}

	wantReason(t, e.groupUC.Leave(ctx, 1, 100), "")
	if e.role(100, 1) != 0 || e.group(t, 100).MemberCount != 2 {
		t.Fatal("old owner did not leave")
	}
	evs := e.events.take()
	if ev, ok := evs[0].msg.(*v1.GroupMemberLeft); len(evs) != 1 || !ok || ev.GetReason() != v1.LeaveReason_LEAVE_REASON_LEFT {
		t.Fatalf("events = %v", eventTypes(evs))
	}
}

// 解散：群主或系统管理员；状态改为已解散，删除成员，待处理申请失效，事件带解散前的成员。
func TestDismissGroup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeApproval, []int64{2}, []int64{3})
	req, _ := e.groupUC.Join(ctx, 4, 100, "")
	e.events.take()

	wantReason(t, e.groupUC.Dismiss(ctx, 2, false, 100), "GROUP_PERMISSION_DENIED")
	wantReason(t, e.groupUC.Dismiss(ctx, 5, false, 100), "NOT_GROUP_MEMBER")
	wantReason(t, e.groupUC.Dismiss(ctx, 1, false, 100), "")
	g := e.group(t, 100)
	if g.Status != GroupDismissed || g.MemberCount != 0 || g.DismissedAt == nil {
		t.Fatalf("group after dismiss = %+v", g)
	}
	if len(e.members.all(100)) != 0 {
		t.Fatal("members not deleted")
	}
	if r, _ := e.joins.FindByID(ctx, req.Request.ID); r.Status != RequestExpired {
		t.Fatalf("pending join request status = %d, want expired", r.Status)
	}
	evs := e.events.take()
	if ev, ok := evs[0].msg.(*v1.GroupDismissed); len(evs) != 1 || !ok || !slices.Equal(ev.GetMemberIds(), []int64{1, 2, 3}) {
		t.Fatalf("events = %v", eventTypes(evs))
	}

	// 解散后仍可查询，但不能再加入或操作
	got, role, err := e.groupUC.Get(ctx, 1, 100)
	if err != nil || got.Status != GroupDismissed || role != 0 {
		t.Fatalf("Get dismissed = %+v, role %d, %v", got, role, err)
	}
	_, err = e.groupUC.Join(ctx, 5, 100, "")
	wantReason(t, err, "GROUP_DISMISSED")
	wantReason(t, e.groupUC.Dismiss(ctx, 1, false, 100), "GROUP_DISMISSED")
	_, err = e.groupUC.ListAllMembers(ctx, 100)
	wantReason(t, err, "GROUP_DISMISSED")
}

// 系统管理员可以解散任意群，不需要是成员。
func TestSystemAdminDismiss(t *testing.T) {
	e := newEnv(t)
	e.newGroup(100, 1, JoinModeFree, nil, nil)
	wantReason(t, e.groupUC.Dismiss(context.Background(), 9, true, 100), "")
	if e.group(t, 100).Status != GroupDismissed {
		t.Fatal("not dismissed")
	}
}

// 邀请：只能邀请自己的好友，已是成员的跳过，被邀请人的待处理申请失效。
func TestAddMembers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeApproval, []int64{2}, []int64{3})
	e.befriend(2, 4)
	e.befriend(2, 5)
	req, _ := e.groupUC.Join(ctx, 4, 100, "")
	e.events.take()

	_, err := e.groupUC.AddMembers(ctx, 3, 100, []int64{4})
	wantReason(t, err, "GROUP_PERMISSION_DENIED")
	_, err = e.groupUC.AddMembers(ctx, 2, 100, []int64{4, 6})
	wantReason(t, err, "NOT_FRIENDS")

	// 3 已是成员（即使不是 2 的好友）跳过
	added, err := e.groupUC.AddMembers(ctx, 2, 100, []int64{3, 4, 5, 4})
	if err != nil || !slices.Equal(added, []int64{4, 5}) {
		t.Fatalf("AddMembers = %v, %v", added, err)
	}
	if e.group(t, 100).MemberCount != 5 {
		t.Fatalf("member count = %d, want 5", e.group(t, 100).MemberCount)
	}
	if m, _ := e.members.Find(ctx, 100, 5); m.InviterID != 2 {
		t.Fatalf("inviter = %d, want 2", m.InviterID)
	}
	if r, _ := e.joins.FindByID(ctx, req.Request.ID); r.Status != RequestExpired {
		t.Fatalf("invitee's pending request status = %d, want expired", r.Status)
	}
	if got := eventTypes(e.events.take()); !slices.Equal(got, []string{"GroupMembersJoined"}) {
		t.Fatalf("events = %v", got)
	}
	added, err = e.groupUC.AddMembers(ctx, 2, 100, []int64{4})
	if err != nil || len(added) != 0 || len(e.events.take()) != 0 {
		t.Fatalf("re-adding members = %v, %v; want no-op", added, err)
	}

	p := testPolicy()
	p.MaxGroupMembers = 5
	e.policy.Store(p)
	e.befriend(2, 6)
	_, err = e.groupUC.AddMembers(ctx, 2, 100, []int64{6})
	wantReason(t, err, "GROUP_FULL")
}

// 非成员看不到群公告。
func TestGetGroupHidesNoticeFromNonMembers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, nil)
	e.groups.byID[100].Notice = "members only"

	g, role, err := e.groupUC.Get(ctx, 1, 100)
	if err != nil || g.Notice != "members only" || role != RoleOwner {
		t.Fatalf("member Get = %+v, role %d, %v", g, role, err)
	}
	g, role, err = e.groupUC.Get(ctx, 2, 100)
	if err != nil || g.Notice != "" || role != 0 {
		t.Fatalf("non-member Get = %+v, role %d, %v", g, role, err)
	}
	if e.groups.byID[100].Notice != "members only" {
		t.Fatal("hiding notice modified the stored group")
	}
	_, _, err = e.groupUC.Get(ctx, 1, 404)
	wantReason(t, err, "GROUP_NOT_FOUND")
}

// 我的群按群 ID 分页，带本人角色。
func TestListMyGroups(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, []int64{2})
	e.newGroup(200, 2, JoinModeFree, nil, nil)
	e.newGroup(300, 3, JoinModeFree, []int64{2}, nil)

	page, next, err := e.groupUC.ListMine(ctx, 2, 0, 2)
	if err != nil || len(page) != 2 || page[0].Group.ID != 100 || page[0].Role != RoleMember || page[1].Role != RoleOwner || next != 200 {
		t.Fatalf("page1 = %+v, next %d, %v", page, next, err)
	}
	page, next, _ = e.groupUC.ListMine(ctx, 2, next, 2)
	if len(page) != 1 || page[0].Group.ID != 300 || page[0].Role != RoleAdmin || next != 0 {
		t.Fatalf("page2 = %+v, next %d", page, next)
	}
}

// 群成员列表仅成员可见。
func TestListMembersRequiresMembership(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.newGroup(100, 1, JoinModeFree, nil, []int64{2, 3})
	list, next, err := e.groupUC.ListMembers(ctx, 2, 100, 0, 2)
	if err != nil || len(list) != 2 || list[0].UserID != 1 || next != 2 {
		t.Fatalf("ListMembers = %+v, next %d, %v", list, next, err)
	}
	_, _, err = e.groupUC.ListMembers(ctx, 4, 100, 0, 0)
	wantReason(t, err, "NOT_GROUP_MEMBER")
}
