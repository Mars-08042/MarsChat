package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
)

// GroupUsecase 负责群资料、成员、入群申请与角色。
//
// 成员变化（入群、邀请、移除、退群、角色、禁言、转让、解散）都在事务内先锁住群行，
// 同一个群的成员变化因此串行，member_count 与成员行数一致，满员检查不会被并发绕过。
// 锁住群行之后的普通读取才建立事务快照，所以能看到之前持锁事务提交的全部修改。
// 资料修改不加行锁，用版本号乐观锁：编辑跨越用户思考时间，不能长时间持锁。
type GroupUsecase struct {
	tx       Transaction
	groups   GroupRepo
	members  MemberRepo
	requests JoinRequestRepo
	friends  FriendRepo
	events   EventRepo
	ids      IDGenerator
	limiter  RateLimiter
	policy   *PolicyHolder
	logger   *slog.Logger
	now      func() time.Time
}

// NewGroupUsecase 创建群用例。
func NewGroupUsecase(tx Transaction, groups GroupRepo, members MemberRepo, requests JoinRequestRepo, friends FriendRepo,
	events EventRepo, ids IDGenerator, limiter RateLimiter, policy *PolicyHolder, logger *slog.Logger) *GroupUsecase {
	return &GroupUsecase{
		tx: tx, groups: groups, members: members, requests: requests, friends: friends, events: events,
		ids: ids, limiter: limiter, policy: policy, logger: logger, now: nowUTC,
	}
}

// CreateGroupInput 是建群参数。
type CreateGroupInput struct {
	Name      string
	Avatar    string
	Notice    string
	JoinMode  int32
	MemberIDs []int64
}

// normalizeName 去掉首尾空白，全是空白时视为参数错误。
func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrInvalidArgument("name: must not be blank")
	}
	return name, nil
}

// Create 建群，创建者为群主；初始成员必须都是创建者的好友。
func (uc *GroupUsecase) Create(ctx context.Context, owner int64, in CreateGroupInput) (*Group, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return nil, err
	}
	members := without(uniqueIDs(in.MemberIDs), owner)
	p := uc.policy.Load()
	if len(members) > int(p.GetMaxInitialMembers()) {
		return nil, ErrInvalidArgument(fmt.Sprintf("member_ids: at most %d initial members", p.GetMaxInitialMembers()))
	}
	if 1+len(members) > int(p.GetMaxGroupMembers()) {
		return nil, ErrGroupFull()
	}
	if err := checkRate(ctx, uc.limiter, uc.logger, "group_create", owner, p.GetGroupCreate()); err != nil {
		return nil, err
	}
	id, err := uc.ids.NextID()
	if err != nil {
		return nil, err
	}
	now := uc.now()
	g := &Group{
		ID: id, Name: name, Avatar: in.Avatar, Notice: in.Notice, OwnerID: owner, JoinMode: in.JoinMode,
		MemberCount: int32(1 + len(members)), Status: GroupNormal, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		if err := uc.requireFriends(ctx, owner, members); err != nil {
			return err
		}
		if err := uc.groups.Create(ctx, g); err != nil {
			return err
		}
		ms := make([]*Member, 0, 1+len(members))
		ms = append(ms, &Member{GroupID: id, UserID: owner, Role: RoleOwner, JoinedAt: now})
		for _, uid := range members {
			ms = append(ms, &Member{GroupID: id, UserID: uid, Role: RoleMember, InviterID: owner, JoinedAt: now})
		}
		if err := uc.members.Add(ctx, ms); err != nil {
			return err
		}
		return uc.events.Add(ctx, TopicSocialEvents, idKey(id), &v1.GroupCreated{
			Group: GroupProto(g), MemberIds: append([]int64{owner}, members...),
		}, now)
	})
	if err != nil {
		return nil, err
	}
	uc.groups.OnCreated(ctx, id)
	return g, nil
}

// Get 返回群资料和本人角色（不是成员时为 0）。非成员看不到群公告。
func (uc *GroupUsecase) Get(ctx context.Context, me, id int64) (*Group, int32, error) {
	g, err := uc.cachedGroup(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	role, err := uc.roleOf(ctx, id, me)
	if err != nil {
		return nil, 0, err
	}
	if role == 0 {
		cp := *g
		cp.Notice = ""
		g = &cp
	}
	return g, role, nil
}

// Update 修改群资料（群主、管理员），返回修改后的资料和本人角色。
// version 与当前版本不一致时返回 GROUP_VERSION_CONFLICT。
func (uc *GroupUsecase) Update(ctx context.Context, me, id int64, version int32, upd GroupUpdate) (*Group, int32, error) {
	if upd.Name != nil {
		name, err := normalizeName(*upd.Name)
		if err != nil {
			return nil, 0, err
		}
		upd.Name = &name
	}
	now := uc.now()
	var (
		g       *Group
		role    int32
		changed bool
	)
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		cur, err := uc.groups.FindByID(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return ErrGroupNotFound()
		}
		if err != nil {
			return err
		}
		if cur.Status == GroupDismissed {
			return ErrGroupDismissed()
		}
		actor, err := uc.actor(ctx, id, me)
		if err != nil {
			return err
		}
		if actor.Role < RoleAdmin {
			return ErrGroupPermissionDenied()
		}
		role = actor.Role
		if upd.empty() {
			if cur.Version != version {
				return ErrGroupVersionConflict()
			}
			g = cur
			return nil
		}
		ok, err := uc.groups.UpdateProfile(ctx, id, version, upd, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrGroupVersionConflict()
		}
		changed = true
		if g, err = uc.groups.FindByID(ctx, id); err != nil {
			return err
		}
		return uc.events.Add(ctx, TopicSocialEvents, idKey(id), &v1.GroupUpdated{Group: GroupProto(g), OperatorId: me}, now)
	})
	if err != nil {
		return nil, 0, err
	}
	if changed {
		uc.groups.EvictCache(ctx, id)
	}
	return g, role, nil
}

// ListMine 按群 ID 递增列出我加入的群。
func (uc *GroupUsecase) ListMine(ctx context.Context, me, cursor int64, limit int32) ([]*MemberGroup, int64, error) {
	n := pageLimit(limit)
	list, err := uc.members.ListByUser(ctx, me, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	list, next := paginate(list, n, func(m *Member) int64 { return m.GroupID })
	ids := make([]int64, len(list))
	for i, m := range list {
		ids[i] = m.GroupID
	}
	groups, err := uc.groups.BatchGetCached(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[int64]*Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	out := make([]*MemberGroup, 0, len(list))
	for _, m := range list {
		if g, ok := byID[m.GroupID]; ok {
			out = append(out, &MemberGroup{Group: g, Role: m.Role})
		}
	}
	return out, next, nil
}

// JoinResult 是加入群的结果。
type JoinResult struct {
	Joined bool
	// Request 是需审核的群中新建或更新的待处理申请
	Request *JoinRequest
}

// Join 加入群：直接加入的群立即入群；需审核的群生成入群申请，已有待处理申请时更新附言。
func (uc *GroupUsecase) Join(ctx context.Context, me, id int64, message string) (*JoinResult, error) {
	p := uc.policy.Load()
	if err := checkRate(ctx, uc.limiter, uc.logger, "join_request", me, p.GetJoinRequest()); err != nil {
		return nil, err
	}
	now := uc.now()
	var res JoinResult
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		g, err := uc.lockGroup(ctx, id)
		if err != nil {
			return err
		}
		member, err := uc.isMember(ctx, id, me)
		if err != nil {
			return err
		}
		if member {
			return ErrAlreadyGroupMember()
		}
		if g.MemberCount >= p.GetMaxGroupMembers() {
			return ErrGroupFull()
		}
		if g.JoinMode == JoinModeFree {
			res.Joined = true
			return uc.addMembers(ctx, id, []int64{me}, 0, me, now)
		}

		pending, err := uc.requests.FindPending(ctx, id, me)
		switch {
		case err == nil:
			if err := uc.requests.Refresh(ctx, pending.ID, message, now); err != nil {
				return err
			}
			pending.Message, pending.UpdatedAt = message, now
			res.Request = pending
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}
		rid, err := uc.ids.NextID()
		if err != nil {
			return err
		}
		r := &JoinRequest{
			ID: rid, GroupID: id, UserID: me, Message: message, Status: RequestPending, CreatedAt: now, UpdatedAt: now,
		}
		if err := uc.requests.Create(ctx, r); err != nil {
			return err
		}
		res.Request = r
		return uc.events.Add(ctx, TopicSocialEvents, idKey(id), &v1.GroupJoinRequestCreated{
			RequestId: rid, GroupId: id, UserId: me, Message: message, CreatedAt: timestamppb.New(now),
		}, now)
	})
	if err != nil {
		return nil, err
	}
	if res.Joined {
		uc.evict(ctx, id)
	}
	return &res, nil
}

// ListJoinRequests 按时间倒序列出群的待处理入群申请（群主、管理员）。
func (uc *GroupUsecase) ListJoinRequests(ctx context.Context, me, groupID, cursor int64, limit int32) ([]*JoinRequest, int64, error) {
	if _, err := uc.cachedGroup(ctx, groupID); err != nil {
		return nil, 0, err
	}
	actor, err := uc.actor(ctx, groupID, me)
	if err != nil {
		return nil, 0, err
	}
	if actor.Role < RoleAdmin {
		return nil, 0, ErrGroupPermissionDenied()
	}
	n := pageLimit(limit)
	list, err := uc.requests.ListPending(ctx, groupID, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	items, next := paginate(list, n, func(r *JoinRequest) int64 { return r.ID })
	return items, next, nil
}

// HandleJoinRequest 同意或拒绝入群申请（群主、管理员）。
func (uc *GroupUsecase) HandleJoinRequest(ctx context.Context, me, requestID int64, accept bool) error {
	r, err := uc.requests.FindByID(ctx, requestID)
	if errors.Is(err, ErrNotFound) {
		return ErrJoinRequestNotFound()
	}
	if err != nil {
		return err
	}
	now := uc.now()
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		g, err := uc.lockGroup(ctx, r.GroupID)
		if err != nil {
			return err
		}
		actor, err := uc.actor(ctx, g.ID, me)
		if err != nil {
			return err
		}
		if actor.Role < RoleAdmin {
			return ErrGroupPermissionDenied()
		}
		// 锁住群行后重新读取：事务外的读取可能早于其他管理员的处理
		cur, err := uc.requests.FindByID(ctx, requestID)
		if err != nil {
			return err
		}
		if cur.Status != RequestPending {
			return ErrRequestAlreadyHandled()
		}
		if !accept {
			return uc.handleJoin(ctx, requestID, RequestRejected, me, now)
		}
		if g.MemberCount >= uc.policy.Load().GetMaxGroupMembers() {
			return ErrGroupFull()
		}
		if err := uc.handleJoin(ctx, requestID, RequestAccepted, me, now); err != nil {
			return err
		}
		return uc.addMembers(ctx, g.ID, []int64{cur.UserID}, 0, me, now)
	})
	if err != nil {
		return err
	}
	if accept {
		uc.evict(ctx, r.GroupID)
	}
	return nil
}

func (uc *GroupUsecase) handleJoin(ctx context.Context, id int64, status int32, handledBy int64, now time.Time) error {
	ok, err := uc.requests.Handle(ctx, id, status, handledBy, now)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRequestAlreadyHandled()
	}
	return nil
}

// AddMembers 邀请好友入群（群主、管理员），不需要审核；已是成员的人跳过。返回实际加入的用户。
func (uc *GroupUsecase) AddMembers(ctx context.Context, me, groupID int64, userIDs []int64) ([]int64, error) {
	userIDs = uniqueIDs(userIDs)
	now := uc.now()
	var added []int64
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		g, err := uc.lockGroup(ctx, groupID)
		if err != nil {
			return err
		}
		actor, err := uc.actor(ctx, groupID, me)
		if err != nil {
			return err
		}
		if actor.Role < RoleAdmin {
			return ErrGroupPermissionDenied()
		}
		existing, err := uc.members.FilterMembers(ctx, groupID, userIDs)
		if err != nil {
			return err
		}
		added = subtract(userIDs, existing)
		if len(added) == 0 {
			return nil
		}
		if err := uc.requireFriends(ctx, me, added); err != nil {
			return err
		}
		if int(g.MemberCount)+len(added) > int(uc.policy.Load().GetMaxGroupMembers()) {
			return ErrGroupFull()
		}
		return uc.addMembers(ctx, groupID, added, me, me, now)
	})
	if err != nil {
		return nil, err
	}
	if len(added) > 0 {
		uc.evict(ctx, groupID)
	}
	return added, nil
}

// addMembers 在已锁住群行的事务中加入成员：写成员行、更新成员数、
// 让这些人对该群的待处理入群申请失效、写事件。inviterID 为 0 表示自己加入。
func (uc *GroupUsecase) addMembers(ctx context.Context, groupID int64, userIDs []int64, inviterID, operatorID int64, now time.Time) error {
	ms := make([]*Member, len(userIDs))
	for i, uid := range userIDs {
		ms[i] = &Member{GroupID: groupID, UserID: uid, Role: RoleMember, InviterID: inviterID, JoinedAt: now}
	}
	if err := uc.members.Add(ctx, ms); err != nil {
		return err
	}
	if err := uc.groups.AddMemberCount(ctx, groupID, len(userIDs)); err != nil {
		return err
	}
	if err := uc.requests.ExpireForUsers(ctx, groupID, userIDs, now); err != nil {
		return err
	}
	return uc.events.Add(ctx, TopicSocialEvents, idKey(groupID), &v1.GroupMembersJoined{
		GroupId: groupID, UserIds: userIDs, OperatorId: operatorID, JoinedAt: timestamppb.New(now),
	}, now)
}

// ListMembers 按用户 ID 递增列出群成员，仅成员可见。
func (uc *GroupUsecase) ListMembers(ctx context.Context, me, groupID, cursor int64, limit int32) ([]*Member, int64, error) {
	if _, err := uc.cachedGroup(ctx, groupID); err != nil {
		return nil, 0, err
	}
	if _, err := uc.actor(ctx, groupID, me); err != nil {
		return nil, 0, err
	}
	n := pageLimit(limit)
	list, err := uc.members.List(ctx, groupID, cursor, n+1)
	if err != nil {
		return nil, 0, err
	}
	items, next := paginate(list, n, func(m *Member) int64 { return m.UserID })
	return items, next, nil
}

// canManage 判断 actor 能否移除或禁言 target：群主可以管理管理员和成员，管理员只能管理成员。
func canManage(actor, target int32) bool {
	return actor >= RoleAdmin && actor > target
}

// RemoveMember 移除群成员。退群请用 Leave。
func (uc *GroupUsecase) RemoveMember(ctx context.Context, me, groupID, userID int64) error {
	if me == userID {
		return ErrCannotTargetSelf()
	}
	now := uc.now()
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		actor, target, err := uc.lockActorTarget(ctx, groupID, me, userID)
		if err != nil {
			return err
		}
		if !canManage(actor.Role, target.Role) {
			return ErrGroupPermissionDenied()
		}
		return uc.removeMember(ctx, groupID, userID, LeaveReasonRemoved, me, now)
	})
	if err != nil {
		return err
	}
	uc.evict(ctx, groupID)
	return nil
}

func (uc *GroupUsecase) removeMember(ctx context.Context, groupID, userID int64, reason int32, operatorID int64, now time.Time) error {
	removed, err := uc.members.Remove(ctx, groupID, userID)
	if err != nil {
		return err
	}
	if !removed {
		// 已锁住群行并读到了成员行，不应发生
		return ErrMemberNotFound()
	}
	if err := uc.groups.AddMemberCount(ctx, groupID, -1); err != nil {
		return err
	}
	return uc.events.Add(ctx, TopicSocialEvents, idKey(groupID), &v1.GroupMemberLeft{
		GroupId: groupID, UserId: userID, Reason: v1.LeaveReason(reason), OperatorId: operatorID, LeftAt: timestamppb.New(now),
	}, now)
}

// SetRole 设置或取消管理员（群主）。role 只能是成员或管理员。
func (uc *GroupUsecase) SetRole(ctx context.Context, me, groupID, userID int64, role int32) error {
	if me == userID {
		return ErrCannotTargetSelf()
	}
	if role != RoleMember && role != RoleAdmin {
		return ErrInvalidArgument("role: must be member or admin")
	}
	now := uc.now()
	changed := false
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		actor, target, err := uc.lockActorTarget(ctx, groupID, me, userID)
		if err != nil {
			return err
		}
		if actor.Role != RoleOwner {
			return ErrGroupPermissionDenied()
		}
		if target.Role == role {
			return nil
		}
		if err := uc.members.SetRole(ctx, groupID, userID, role); err != nil {
			return err
		}
		changed = true
		return uc.memberUpdated(ctx, groupID, userID, role, target.MutedUntil, me, now)
	})
	if err != nil {
		return err
	}
	if changed {
		uc.members.EvictCache(ctx, groupID)
	}
	return nil
}

// Mute 禁言 d 时长，d 为 0 时解除禁言。返回禁言截止时间，解除时为 nil。
func (uc *GroupUsecase) Mute(ctx context.Context, me, groupID, userID int64, d time.Duration) (*time.Time, error) {
	if me == userID {
		return nil, ErrCannotTargetSelf()
	}
	now := uc.now()
	var until *time.Time
	if d > 0 {
		t := now.Add(d)
		until = &t
	}
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		actor, target, err := uc.lockActorTarget(ctx, groupID, me, userID)
		if err != nil {
			return err
		}
		if !canManage(actor.Role, target.Role) {
			return ErrGroupPermissionDenied()
		}
		if err := uc.members.SetMutedUntil(ctx, groupID, userID, until); err != nil {
			return err
		}
		return uc.memberUpdated(ctx, groupID, userID, target.Role, until, me, now)
	})
	if err != nil {
		return nil, err
	}
	uc.members.EvictCache(ctx, groupID)
	return until, nil
}

func (uc *GroupUsecase) memberUpdated(ctx context.Context, groupID, userID int64, role int32, mutedUntil *time.Time, operatorID int64, now time.Time) error {
	return uc.events.Add(ctx, TopicSocialEvents, idKey(groupID), &v1.GroupMemberUpdated{
		GroupId: groupID, UserId: userID, Role: v1.GroupRole(role), MutedUntil: OptionalTimestamp(mutedUntil),
		OperatorId: operatorID, UpdatedAt: timestamppb.New(now),
	}, now)
}

// Transfer 转让群主（群主）。新群主必须是成员，原群主变为管理员。
func (uc *GroupUsecase) Transfer(ctx context.Context, me, groupID, newOwner int64) error {
	if me == newOwner {
		return ErrCannotTargetSelf()
	}
	now := uc.now()
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		actor, _, err := uc.lockActorTarget(ctx, groupID, me, newOwner)
		if err != nil {
			return err
		}
		if actor.Role != RoleOwner {
			return ErrGroupPermissionDenied()
		}
		if err := uc.members.SetRole(ctx, groupID, newOwner, RoleOwner); err != nil {
			return err
		}
		// 群主不能被禁言，新群主之前的禁言一并解除
		if err := uc.members.SetMutedUntil(ctx, groupID, newOwner, nil); err != nil {
			return err
		}
		if err := uc.members.SetRole(ctx, groupID, me, RoleAdmin); err != nil {
			return err
		}
		if err := uc.groups.SetOwner(ctx, groupID, newOwner, now); err != nil {
			return err
		}
		g, err := uc.groups.FindByID(ctx, groupID)
		if err != nil {
			return err
		}
		if err := uc.memberUpdated(ctx, groupID, newOwner, RoleOwner, nil, me, now); err != nil {
			return err
		}
		if err := uc.memberUpdated(ctx, groupID, me, RoleAdmin, nil, me, now); err != nil {
			return err
		}
		return uc.events.Add(ctx, TopicSocialEvents, idKey(groupID), &v1.GroupUpdated{Group: GroupProto(g), OperatorId: me}, now)
	})
	if err != nil {
		return err
	}
	uc.evict(ctx, groupID)
	return nil
}

// Leave 退出群。群主需要先转让群主。
func (uc *GroupUsecase) Leave(ctx context.Context, me, groupID int64) error {
	now := uc.now()
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := uc.lockGroup(ctx, groupID); err != nil {
			return err
		}
		actor, err := uc.actor(ctx, groupID, me)
		if err != nil {
			return err
		}
		if actor.Role == RoleOwner {
			return ErrOwnerCannotLeave()
		}
		return uc.removeMember(ctx, groupID, me, LeaveReasonLeft, me, now)
	})
	if err != nil {
		return err
	}
	uc.evict(ctx, groupID)
	return nil
}

// Dismiss 解散群（群主；系统管理员可以解散任意群）。删除全部成员、让待处理申请失效，
// 群资料保留并标记为已解散，事件带上解散前的成员。
func (uc *GroupUsecase) Dismiss(ctx context.Context, me int64, systemAdmin bool, groupID int64) error {
	now := uc.now()
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := uc.lockGroup(ctx, groupID); err != nil {
			return err
		}
		if !systemAdmin {
			actor, err := uc.actor(ctx, groupID, me)
			if err != nil {
				return err
			}
			if actor.Role != RoleOwner {
				return ErrGroupPermissionDenied()
			}
		}
		memberIDs, err := uc.members.RemoveAll(ctx, groupID)
		if err != nil {
			return err
		}
		if err := uc.groups.Dismiss(ctx, groupID, now); err != nil {
			return err
		}
		if err := uc.requests.ExpireAll(ctx, groupID, now); err != nil {
			return err
		}
		return uc.events.Add(ctx, TopicSocialEvents, idKey(groupID), &v1.GroupDismissed{
			GroupId: groupID, MemberIds: memberIDs, OperatorId: me, DismissedAt: timestamppb.New(now),
		}, now)
	})
	if err != nil {
		return err
	}
	uc.evict(ctx, groupID)
	return nil
}

// ListAllMembers 返回全部成员（只有用户 ID、角色、禁言截止），走成员列表缓存。供 delivery 投递群消息。
func (uc *GroupUsecase) ListAllMembers(ctx context.Context, groupID int64) ([]*Member, error) {
	g, err := uc.cachedGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if g.Status == GroupDismissed {
		return nil, ErrGroupDismissed()
	}
	return uc.members.GetAllCached(ctx, groupID)
}

// BatchGet 批量获取群资料，不存在的 ID 跳过。
func (uc *GroupUsecase) BatchGet(ctx context.Context, ids []int64) ([]*Group, error) {
	return uc.groups.BatchGetCached(ctx, ids)
}

// lockGroup 锁住群行，群不存在或已解散时返回对应的业务错误。
func (uc *GroupUsecase) lockGroup(ctx context.Context, id int64) (*Group, error) {
	g, err := uc.groups.Lock(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrGroupNotFound()
	}
	if err != nil {
		return nil, err
	}
	if g.Status == GroupDismissed {
		return nil, ErrGroupDismissed()
	}
	return g, nil
}

// lockActorTarget 锁住群行，再读取操作者与操作对象的成员记录。
func (uc *GroupUsecase) lockActorTarget(ctx context.Context, groupID, actorID, targetID int64) (*Member, *Member, error) {
	if _, err := uc.lockGroup(ctx, groupID); err != nil {
		return nil, nil, err
	}
	actor, err := uc.actor(ctx, groupID, actorID)
	if err != nil {
		return nil, nil, err
	}
	target, err := uc.members.Find(ctx, groupID, targetID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, ErrMemberNotFound()
	}
	if err != nil {
		return nil, nil, err
	}
	return actor, target, nil
}

// actor 返回操作者的成员记录，不是成员时返回 NOT_GROUP_MEMBER。
func (uc *GroupUsecase) actor(ctx context.Context, groupID, userID int64) (*Member, error) {
	m, err := uc.members.Find(ctx, groupID, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotGroupMember()
	}
	return m, err
}

func (uc *GroupUsecase) isMember(ctx context.Context, groupID, userID int64) (bool, error) {
	_, err := uc.members.Find(ctx, groupID, userID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (uc *GroupUsecase) roleOf(ctx context.Context, groupID, userID int64) (int32, error) {
	m, err := uc.members.Find(ctx, groupID, userID)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return m.Role, nil
}

func (uc *GroupUsecase) cachedGroup(ctx context.Context, id int64) (*Group, error) {
	g, err := uc.groups.GetCached(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrGroupNotFound()
	}
	return g, err
}

// requireFriends 要求 ids（已去重）都是 userID 的好友。
func (uc *GroupUsecase) requireFriends(ctx context.Context, userID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	friends, err := uc.friends.FilterFriends(ctx, userID, ids)
	if err != nil {
		return err
	}
	if len(friends) != len(ids) {
		return ErrNotFriends()
	}
	return nil
}

// evict 在成员变化提交后删除群资料（含成员数）和成员列表缓存。
func (uc *GroupUsecase) evict(ctx context.Context, groupID int64) {
	uc.groups.EvictCache(ctx, groupID)
	uc.members.EvictCache(ctx, groupID)
}

func without(ids []int64, x int64) []int64 {
	out := ids[:0:0]
	for _, id := range ids {
		if id != x {
			out = append(out, id)
		}
	}
	return out
}

// subtract 返回 ids 中不在 remove 里的元素，保持原顺序。
func subtract(ids, remove []int64) []int64 {
	skip := make(map[int64]struct{}, len(remove))
	for _, id := range remove {
		skip[id] = struct{}{}
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := skip[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
