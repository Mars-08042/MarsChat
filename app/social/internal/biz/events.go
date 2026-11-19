package biz

import (
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
)

// idKey 是事件的分区键：群事件用群 ID，申请事件用接收方 ID。
func idKey(id int64) string {
	return strconv.FormatInt(id, 10)
}

// pairKey 是好友事件的分区键：两人中较小的 ID，同一对用户的事件落在同一分区、保持有序。
func pairKey(a, b int64) string {
	return idKey(min(a, b))
}

// GroupProto 把群资料转换成 proto，my_role 不填；service 层在此基础上补本人角色、按权限隐藏公告。
func GroupProto(g *Group) *v1.Group {
	return &v1.Group{
		Id:          g.ID,
		Name:        g.Name,
		Avatar:      g.Avatar,
		Notice:      g.Notice,
		OwnerId:     g.OwnerID,
		JoinMode:    v1.JoinMode(g.JoinMode),
		MuteAll:     g.MuteAll,
		MemberCount: g.MemberCount,
		Status:      v1.GroupStatus(g.Status),
		Version:     g.Version,
		CreatedAt:   timestamppb.New(g.CreatedAt),
		UpdatedAt:   timestamppb.New(g.UpdatedAt),
	}
}

// OptionalTimestamp 把可空时间转换成 proto，nil 时不填。
func OptionalTimestamp(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
