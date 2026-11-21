//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	socialv1 "github.com/Mars-08042/MarsChat/api/social/v1"
)

// social 经 Consul 发现并调用 account（申请好友时确认对方存在、列表补全资料），所以两个服务都要启动。
const (
	socialHTTP = "http://127.0.0.1:18020"
	socialGRPC = "127.0.0.1:19020"
)

func socialCall(t *testing.T, method, path, token string, req, resp proto.Message) (int, *apiError) {
	t.Helper()
	return callAt(t, socialHTTP, method, path, token, req, resp)
}

func mustSocial(t *testing.T, method, path, token string, req, resp proto.Message) {
	t.Helper()
	if status, e := socialCall(t, method, path, token, req, resp); e != nil {
		t.Fatalf("%s %s: status %d, %+v", method, path, status, e)
	}
}

type user struct {
	id    int64
	token string
}

func newUser(t *testing.T) user {
	t.Helper()
	email := randomEmail(t)
	s := register(t, email, "e2e-social-"+localPart(email))
	return user{id: s.user.GetId(), token: s.token.GetAccessToken()}
}

func id(v int64) string { return strconv.FormatInt(v, 10) }

// makeFriends 走申请、同意的完整流程。
func makeFriends(t *testing.T, a, b user) {
	t.Helper()
	var sent socialv1.SendFriendRequestResponse
	mustSocial(t, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: b.id}, &sent)
	mustSocial(t, http.MethodPost, "/v1/friend-requests/"+id(sent.GetRequest().GetId())+"/accept", b.token, &socialv1.AcceptFriendRequestRequest{}, nil)
}

func internalClient(t *testing.T) socialv1.SocialInternalServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(socialGRPC, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return socialv1.NewSocialInternalServiceClient(conn)
}

// checkSend 调用内部接口校验发言权限，返回错误原因，允许时为空。
func checkSend(t *testing.T, c socialv1.SocialInternalServiceClient, sender int64, peerType socialv1.PeerType, peer int64) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.CheckSendPermission(ctx, &socialv1.CheckSendPermissionRequest{SenderId: sender, PeerType: peerType, PeerId: peer})
	if err == nil {
		return ""
	}
	return kerrors.Reason(err)
}

func TestFriendFlow(t *testing.T) {
	resetRateLimits(t)
	a, b, c := newUser(t), newUser(t), newUser(t)
	internal := internalClient(t)

	status, e := socialCall(t, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: a.id}, nil)
	wantError(t, status, e, 400, "CANNOT_TARGET_SELF")
	// 经 Consul 调用 account 确认对方不存在
	status, e = socialCall(t, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: 12345}, nil)
	wantError(t, status, e, 404, "USER_NOT_FOUND")

	var sent socialv1.SendFriendRequestResponse
	mustSocial(t, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: b.id, Message: "hi"}, &sent)
	if sent.GetBecameFriends() || sent.GetRequest().GetStatus() != socialv1.RequestStatus_REQUEST_STATUS_PENDING {
		t.Fatalf("SendFriendRequest = %v", &sent)
	}
	// 重复申请更新附言，仍是同一条
	var again socialv1.SendFriendRequestResponse
	mustSocial(t, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: b.id, Message: "hello"}, &again)
	if again.GetRequest().GetId() != sent.GetRequest().GetId() {
		t.Fatalf("repeated request id = %d, want %d", again.GetRequest().GetId(), sent.GetRequest().GetId())
	}

	var received socialv1.ListReceivedFriendRequestsResponse
	mustSocial(t, http.MethodGet, "/v1/friend-requests/received", b.token, nil, &received)
	if len(received.GetRequests()) != 1 || received.GetRequests()[0].GetMessage() != "hello" ||
		received.GetRequests()[0].GetPeer().GetNickname() != "小克" {
		t.Fatalf("received = %v", received.GetRequests())
	}
	reqPath := "/v1/friend-requests/" + id(sent.GetRequest().GetId())
	status, e = socialCall(t, http.MethodPost, reqPath+"/accept", a.token, &socialv1.AcceptFriendRequestRequest{}, nil)
	wantError(t, status, e, 404, "FRIEND_REQUEST_NOT_FOUND")
	if got := checkSend(t, internal, a.id, socialv1.PeerType_PEER_TYPE_USER, b.id); got != "NOT_FRIENDS" {
		t.Fatalf("check send before accept = %q, want NOT_FRIENDS", got)
	}
	mustSocial(t, http.MethodPost, reqPath+"/accept", b.token, &socialv1.AcceptFriendRequestRequest{}, nil)
	status, e = socialCall(t, http.MethodPost, reqPath+"/accept", b.token, &socialv1.AcceptFriendRequestRequest{}, nil)
	wantError(t, status, e, 409, "REQUEST_ALREADY_HANDLED")

	var friends socialv1.ListFriendsResponse
	mustSocial(t, http.MethodGet, "/v1/friends", a.token, nil, &friends)
	if len(friends.GetFriends()) != 1 || friends.GetFriends()[0].GetUserId() != b.id || friends.GetFriends()[0].GetProfile().GetId() != b.id {
		t.Fatalf("friends = %v", friends.GetFriends())
	}
	if got := checkSend(t, internal, a.id, socialv1.PeerType_PEER_TYPE_USER, b.id); got != "" {
		t.Fatalf("check send between friends = %q", got)
	}

	// 互相申请自动成为好友
	mustSocial(t, http.MethodPost, "/v1/friend-requests", c.token, &socialv1.SendFriendRequestRequest{UserId: a.id}, nil)
	var mutual socialv1.SendFriendRequestResponse
	mustSocial(t, http.MethodPost, "/v1/friend-requests", a.token, &socialv1.SendFriendRequestRequest{UserId: c.id}, &mutual)
	if !mutual.GetBecameFriends() {
		t.Fatalf("mutual request = %v, want became_friends", &mutual)
	}

	// 拉黑不删除好友，但双方都不能单聊、不能申请
	mustSocial(t, http.MethodPost, "/v1/blocks", b.token, &socialv1.BlockUserRequest{UserId: a.id}, nil)
	if got := checkSend(t, internal, a.id, socialv1.PeerType_PEER_TYPE_USER, b.id); got != "BLOCKED" {
		t.Fatalf("check send after block = %q, want BLOCKED", got)
	}
	var blocked socialv1.ListBlockedUsersResponse
	mustSocial(t, http.MethodGet, "/v1/blocks", b.token, nil, &blocked)
	if len(blocked.GetUsers()) != 1 || blocked.GetUsers()[0].GetUserId() != a.id {
		t.Fatalf("blocked = %v", blocked.GetUsers())
	}
	mustSocial(t, http.MethodDelete, "/v1/blocks/"+id(a.id), b.token, nil, nil)
	if got := checkSend(t, internal, b.id, socialv1.PeerType_PEER_TYPE_USER, a.id); got != "" {
		t.Fatalf("check send after unblock = %q", got)
	}

	// 删除好友是幂等的
	mustSocial(t, http.MethodDelete, "/v1/friends/"+id(b.id), a.token, nil, nil)
	mustSocial(t, http.MethodDelete, "/v1/friends/"+id(b.id), a.token, nil, nil)
	if got := checkSend(t, internal, b.id, socialv1.PeerType_PEER_TYPE_USER, a.id); got != "NOT_FRIENDS" {
		t.Fatalf("check send after delete = %q, want NOT_FRIENDS", got)
	}
}

func TestGroupFlow(t *testing.T) {
	resetRateLimits(t)
	owner, admin, member, outsider := newUser(t), newUser(t), newUser(t), newUser(t)
	makeFriends(t, owner, admin)
	internal := internalClient(t)

	var created socialv1.CreateGroupResponse
	mustSocial(t, http.MethodPost, "/v1/groups", owner.token, &socialv1.CreateGroupRequest{
		Name: "e2e group", Notice: "members only", JoinMode: socialv1.JoinMode_JOIN_MODE_APPROVAL, MemberIds: []int64{admin.id},
	}, &created)
	g := created.GetGroup()
	if g.GetMemberCount() != 2 || g.GetMyRole() != socialv1.GroupRole_GROUP_ROLE_OWNER || g.GetVersion() != 1 {
		t.Fatalf("created group = %v", g)
	}
	base := "/v1/groups/" + id(g.GetId())
	status, e := socialCall(t, http.MethodPost, "/v1/groups", owner.token, &socialv1.CreateGroupRequest{
		Name: "x", JoinMode: socialv1.JoinMode_JOIN_MODE_FREE, MemberIds: []int64{outsider.id},
	}, nil)
	wantError(t, status, e, 403, "NOT_FRIENDS")

	mustSocial(t, http.MethodPut, base+"/members/"+id(admin.id)+"/role", owner.token,
		&socialv1.SetGroupMemberRoleRequest{Role: socialv1.GroupRole_GROUP_ROLE_ADMIN}, nil)

	// 审核入群：申请 → 管理员同意
	var joined socialv1.JoinGroupResponse
	mustSocial(t, http.MethodPost, base+"/join", member.token, &socialv1.JoinGroupRequest{Message: "let me in"}, &joined)
	if joined.GetJoined() || joined.GetRequest().GetId() == 0 {
		t.Fatalf("join = %v, want pending request", &joined)
	}
	var pending socialv1.ListGroupJoinRequestsResponse
	mustSocial(t, http.MethodGet, base+"/join-requests", admin.token, nil, &pending)
	if len(pending.GetRequests()) != 1 || pending.GetRequests()[0].GetProfile().GetId() != member.id {
		t.Fatalf("join requests = %v", pending.GetRequests())
	}
	joinPath := "/v1/group-join-requests/" + id(joined.GetRequest().GetId())
	status, e = socialCall(t, http.MethodPost, joinPath+"/accept", outsider.token, &socialv1.AcceptGroupJoinRequestRequest{}, nil)
	wantError(t, status, e, 403, "NOT_GROUP_MEMBER")
	mustSocial(t, http.MethodPost, joinPath+"/accept", admin.token, &socialv1.AcceptGroupJoinRequestRequest{}, nil)

	var members socialv1.ListGroupMembersResponse
	mustSocial(t, http.MethodGet, base+"/members", member.token, nil, &members)
	if len(members.GetMembers()) != 3 {
		t.Fatalf("members = %v", members.GetMembers())
	}
	for _, m := range members.GetMembers() {
		if m.GetProfile().GetId() != m.GetUserId() {
			t.Fatalf("member %d without profile", m.GetUserId())
		}
	}
	status, e = socialCall(t, http.MethodGet, base+"/members", outsider.token, nil, nil)
	wantError(t, status, e, 403, "NOT_GROUP_MEMBER")

	// 非成员看不到公告
	var seen socialv1.GetGroupResponse
	mustSocial(t, http.MethodGet, base, outsider.token, nil, &seen)
	if seen.GetGroup().GetNotice() != "" || seen.GetGroup().GetMyRole() != socialv1.GroupRole_GROUP_ROLE_UNSPECIFIED {
		t.Fatalf("outsider sees %v", seen.GetGroup())
	}

	// 发言权限：成员、禁言、全员禁言
	gid := g.GetId()
	if got := checkSend(t, internal, member.id, socialv1.PeerType_PEER_TYPE_GROUP, gid); got != "" {
		t.Fatalf("member check send = %q", got)
	}
	if got := checkSend(t, internal, outsider.id, socialv1.PeerType_PEER_TYPE_GROUP, gid); got != "NOT_GROUP_MEMBER" {
		t.Fatalf("outsider check send = %q", got)
	}
	var muted socialv1.MuteGroupMemberResponse
	mustSocial(t, http.MethodPut, base+"/members/"+id(member.id)+"/mute", admin.token, &socialv1.MuteGroupMemberRequest{DurationSeconds: 600}, &muted)
	if muted.GetMutedUntil() == nil {
		t.Fatal("muted_until missing")
	}
	if got := checkSend(t, internal, member.id, socialv1.PeerType_PEER_TYPE_GROUP, gid); got != "MEMBER_MUTED" {
		t.Fatalf("muted member check send = %q", got)
	}
	status, e = socialCall(t, http.MethodPut, base+"/members/"+id(owner.id)+"/mute", admin.token, &socialv1.MuteGroupMemberRequest{DurationSeconds: 600}, nil)
	wantError(t, status, e, 403, "GROUP_PERMISSION_DENIED")
	mustSocial(t, http.MethodPut, base+"/members/"+id(member.id)+"/mute", admin.token, &socialv1.MuteGroupMemberRequest{}, nil)

	muteAll := true
	var updated socialv1.UpdateGroupResponse
	mustSocial(t, http.MethodPatch, base, admin.token, &socialv1.UpdateGroupRequest{Version: 1, MuteAll: &muteAll}, &updated)
	if updated.GetGroup().GetVersion() != 2 || !updated.GetGroup().GetMuteAll() {
		t.Fatalf("updated = %v", updated.GetGroup())
	}
	if got := checkSend(t, internal, member.id, socialv1.PeerType_PEER_TYPE_GROUP, gid); got != "GROUP_MUTED" {
		t.Fatalf("member check send under mute_all = %q", got)
	}
	if got := checkSend(t, internal, admin.id, socialv1.PeerType_PEER_TYPE_GROUP, gid); got != "" {
		t.Fatalf("admin check send under mute_all = %q", got)
	}
	// 拿旧版本号修改：冲突
	name := "renamed"
	status, e = socialCall(t, http.MethodPatch, base, owner.token, &socialv1.UpdateGroupRequest{Version: 1, Name: &name}, nil)
	wantError(t, status, e, 409, "GROUP_VERSION_CONFLICT")

	// 内部接口：全部成员、批量群资料
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := internal.ListAllGroupMembers(ctx, &socialv1.ListAllGroupMembersRequest{GroupId: gid})
	if err != nil || len(all.GetMembers()) != 3 {
		t.Fatalf("ListAllGroupMembers = %v, %v", all, err)
	}
	batch, err := internal.BatchGetGroups(ctx, &socialv1.BatchGetGroupsRequest{GroupIds: []int64{gid, 12345}})
	if err != nil || len(batch.GetGroups()) != 1 || !batch.GetGroups()[0].GetMuteAll() {
		t.Fatalf("BatchGetGroups = %v, %v", batch, err)
	}

	// 转让、退群、解散
	status, e = socialCall(t, http.MethodPost, base+"/leave", owner.token, &socialv1.LeaveGroupRequest{}, nil)
	wantError(t, status, e, 409, "OWNER_CANNOT_LEAVE")
	mustSocial(t, http.MethodPost, base+"/transfer", owner.token, &socialv1.TransferGroupOwnerRequest{NewOwnerId: admin.id}, nil)
	mustSocial(t, http.MethodPost, base+"/leave", owner.token, &socialv1.LeaveGroupRequest{}, nil)
	var mine socialv1.ListMyGroupsResponse
	mustSocial(t, http.MethodGet, "/v1/groups", admin.token, nil, &mine)
	if len(mine.GetGroups()) != 1 || mine.GetGroups()[0].GetMyRole() != socialv1.GroupRole_GROUP_ROLE_OWNER ||
		mine.GetGroups()[0].GetMemberCount() != 2 {
		t.Fatalf("admin's groups after transfer = %v", mine.GetGroups())
	}
	status, e = socialCall(t, http.MethodPost, base+"/dismiss", member.token, &socialv1.DismissGroupRequest{}, nil)
	wantError(t, status, e, 403, "GROUP_PERMISSION_DENIED")
	mustSocial(t, http.MethodPost, base+"/dismiss", admin.token, &socialv1.DismissGroupRequest{}, nil)
	mustSocial(t, http.MethodGet, base, admin.token, nil, &seen)
	if seen.GetGroup().GetStatus() != socialv1.GroupStatus_GROUP_STATUS_DISMISSED {
		t.Fatalf("group after dismiss = %v", seen.GetGroup())
	}
	if got := checkSend(t, internal, admin.id, socialv1.PeerType_PEER_TYPE_GROUP, gid); got != "GROUP_DISMISSED" {
		t.Fatalf("check send after dismiss = %q", got)
	}
}

// 直接加入的群满员时拒绝。成员数上限默认 500，这里直接把成员数改到上限，再删除群资料缓存。
func TestJoinFullGroup(t *testing.T) {
	resetRateLimits(t)
	owner, joiner, other := newUser(t), newUser(t), newUser(t)
	var created socialv1.CreateGroupResponse
	mustSocial(t, http.MethodPost, "/v1/groups", owner.token, &socialv1.CreateGroupRequest{
		Name: "full", JoinMode: socialv1.JoinMode_JOIN_MODE_FREE,
	}, &created)
	gid := created.GetGroup().GetId()
	base := "/v1/groups/" + id(gid)

	var joined socialv1.JoinGroupResponse
	mustSocial(t, http.MethodPost, base+"/join", joiner.token, &socialv1.JoinGroupRequest{}, &joined)
	if !joined.GetJoined() {
		t.Fatalf("join free group = %v", &joined)
	}
	status, e := socialCall(t, http.MethodPost, base+"/join", joiner.token, &socialv1.JoinGroupRequest{}, nil)
	wantError(t, status, e, 409, "ALREADY_GROUP_MEMBER")

	dsn := fmt.Sprintf("social:%s@tcp(127.0.0.1:13306)/marschat_social?parseTime=true&loc=UTC", os.Getenv("MARSCHAT_SOCIAL_DB_PASSWORD"))
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}
	if err := db.Exec("UPDATE chat_groups SET member_count = 500 WHERE id = ?", gid).Error; err != nil {
		t.Fatal(err)
	}
	newRedisClient(t).Del(context.Background(), "social:group:"+id(gid))
	status, e = socialCall(t, http.MethodPost, base+"/join", other.token, &socialv1.JoinGroupRequest{}, nil)
	wantError(t, status, e, 409, "GROUP_FULL")
}

func TestSocialAuthAndValidation(t *testing.T) {
	resetRateLimits(t)
	status, e := socialCall(t, http.MethodGet, "/v1/friends", "", nil, nil)
	wantError(t, status, e, 401, "UNAUTHORIZED")
	u := newUser(t)
	status, e = socialCall(t, http.MethodPost, "/v1/friend-requests", u.token, &socialv1.SendFriendRequestRequest{UserId: 0}, nil)
	wantError(t, status, e, 400, "VALIDATOR")
	status, e = socialCall(t, http.MethodGet, "/v1/groups/12345", u.token, nil, nil)
	wantError(t, status, e, 404, "GROUP_NOT_FOUND")
}
