package biz

import (
	"context"
	"testing"
	"time"
)

// 单聊：好友且双方都没有拉黑；拉黑优先于好友检查。
func TestCheckSendPrivate(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(e *env)
		peer   int64
		reason string
	}{
		{name: "friends", setup: func(e *env) { e.befriend(1, 2) }, peer: 2},
		{name: "not friends", peer: 2, reason: "NOT_FRIENDS"},
		{name: "self", peer: 1, reason: "CANNOT_TARGET_SELF"},
		{name: "friends but i blocked", setup: func(e *env) {
			e.befriend(1, 2)
			_ = e.blocks.Add(context.Background(), 1, 2, e.now)
		}, peer: 2, reason: "BLOCKED"},
		{name: "friends but blocked by peer", setup: func(e *env) {
			e.befriend(1, 2)
			_ = e.blocks.Add(context.Background(), 2, 1, e.now)
		}, peer: 2, reason: "BLOCKED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			wantReason(t, e.permUC.CheckSend(context.Background(), 1, PeerUser, tt.peer), tt.reason)
		})
	}
}

// 群聊：群正常、是成员、未被单人禁言；全员禁言时只有群主和管理员能发言。
func TestCheckSendGroup(t *testing.T) {
	const owner, admin, member, mutedMember, expiredMute, outsider = 1, 2, 3, 4, 5, 6
	setup := func(t *testing.T) *env {
		e := newEnv(t)
		e.newGroup(100, owner, JoinModeFree, []int64{admin}, []int64{member, mutedMember, expiredMute})
		future, past := e.now.Add(time.Hour), e.now.Add(-time.Second)
		_ = e.members.SetMutedUntil(context.Background(), 100, mutedMember, &future)
		_ = e.members.SetMutedUntil(context.Background(), 100, expiredMute, &past)
		return e
	}
	tests := []struct {
		sender  int64
		muteAll bool
		reason  string
	}{
		{sender: member},
		{sender: expiredMute},
		{sender: mutedMember, reason: "MEMBER_MUTED"},
		{sender: outsider, reason: "NOT_GROUP_MEMBER"},
		{sender: member, muteAll: true, reason: "GROUP_MUTED"},
		{sender: admin, muteAll: true},
		{sender: owner, muteAll: true},
	}
	for _, tt := range tests {
		e := setup(t)
		e.groups.byID[100].MuteAll = tt.muteAll
		err := e.permUC.CheckSend(context.Background(), tt.sender, PeerGroup, 100)
		if tt.reason == "" && err != nil || tt.reason != "" && err == nil {
			t.Errorf("sender %d mute_all %v: err = %v, want reason %q", tt.sender, tt.muteAll, err, tt.reason)
			continue
		}
		if tt.reason != "" {
			wantReason(t, err, tt.reason)
		}
	}

	e := setup(t)
	wantReason(t, e.permUC.CheckSend(context.Background(), member, PeerGroup, 404), "GROUP_NOT_FOUND")
	wantReason(t, e.groupUC.Dismiss(context.Background(), owner, false, 100), "")
	wantReason(t, e.permUC.CheckSend(context.Background(), member, PeerGroup, 100), "GROUP_DISMISSED")
	wantReason(t, e.permUC.CheckSend(context.Background(), member, 9, 100), "VALIDATOR")
}
