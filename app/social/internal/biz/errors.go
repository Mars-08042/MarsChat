package biz

import (
	"math"
	"strconv"
	"time"

	"github.com/go-kratos/kratos/v3/errors"

	v1 "github.com/Mars-08042/MarsChat/api/social/v1"
)

func newErr(code int, reason v1.ErrorReason, message string) *errors.Error {
	return errors.New(code, reason.String(), message)
}

// withRetryAfter 在 429 错误中附带需要等待的秒数，客户端据此显示倒计时。
func withRetryAfter(e *errors.Error, d time.Duration) *errors.Error {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return e.WithMetadata(map[string]string{"retry_after_seconds": strconv.Itoa(secs)})
}

// ErrInvalidArgument 与 protovalidate 中间件的错误格式一致（400，reason 为 VALIDATOR），
// 用于 proto 规则表达不了的参数检查。
func ErrInvalidArgument(message string) error {
	return errors.BadRequest("VALIDATOR", message)
}

func ErrCannotTargetSelf() error {
	return newErr(400, v1.ErrorReason_CANNOT_TARGET_SELF, "不能对自己执行该操作")
}

func ErrFriendRequestNotAllowed() error {
	return newErr(403, v1.ErrorReason_FRIEND_REQUEST_NOT_ALLOWED, "无法添加对方为好友")
}

func ErrNotFriends() error {
	return newErr(403, v1.ErrorReason_NOT_FRIENDS, "对方不是你的好友")
}

func ErrBlocked() error {
	return newErr(403, v1.ErrorReason_BLOCKED, "消息已被拒收")
}

func ErrGroupPermissionDenied() error {
	return newErr(403, v1.ErrorReason_GROUP_PERMISSION_DENIED, "没有权限执行该操作")
}

func ErrNotGroupMember() error {
	return newErr(403, v1.ErrorReason_NOT_GROUP_MEMBER, "你不是该群成员")
}

func ErrMemberMuted() error {
	return newErr(403, v1.ErrorReason_MEMBER_MUTED, "你已被禁言")
}

func ErrGroupMuted() error {
	return newErr(403, v1.ErrorReason_GROUP_MUTED, "群已开启全员禁言")
}

func ErrUserNotFound() error {
	return newErr(404, v1.ErrorReason_USER_NOT_FOUND, "用户不存在")
}

func ErrFriendRequestNotFound() error {
	return newErr(404, v1.ErrorReason_FRIEND_REQUEST_NOT_FOUND, "好友申请不存在")
}

func ErrGroupNotFound() error {
	return newErr(404, v1.ErrorReason_GROUP_NOT_FOUND, "群不存在")
}

func ErrJoinRequestNotFound() error {
	return newErr(404, v1.ErrorReason_GROUP_JOIN_REQUEST_NOT_FOUND, "入群申请不存在")
}

func ErrMemberNotFound() error {
	return newErr(404, v1.ErrorReason_GROUP_MEMBER_NOT_FOUND, "对方不是该群成员")
}

func ErrAlreadyFriends() error {
	return newErr(409, v1.ErrorReason_ALREADY_FRIENDS, "你们已经是好友")
}

func ErrRequestAlreadyHandled() error {
	return newErr(409, v1.ErrorReason_REQUEST_ALREADY_HANDLED, "申请已被处理")
}

func ErrAlreadyGroupMember() error {
	return newErr(409, v1.ErrorReason_ALREADY_GROUP_MEMBER, "已经是群成员")
}

func ErrGroupFull() error {
	return newErr(409, v1.ErrorReason_GROUP_FULL, "群成员已达上限")
}

func ErrGroupDismissed() error {
	return newErr(409, v1.ErrorReason_GROUP_DISMISSED, "群已解散")
}

func ErrGroupVersionConflict() error {
	return newErr(409, v1.ErrorReason_GROUP_VERSION_CONFLICT, "群资料已被他人修改，请刷新后重试")
}

func ErrOwnerCannotLeave() error {
	return newErr(409, v1.ErrorReason_OWNER_CANNOT_LEAVE, "群主需要先转让群主才能退群")
}

func ErrRateLimited(retry time.Duration) error {
	return withRetryAfter(newErr(429, v1.ErrorReason_RATE_LIMITED, "操作过于频繁，请稍后再试"), retry)
}

func ErrDependencyUnavailable() error {
	return newErr(503, v1.ErrorReason_DEPENDENCY_UNAVAILABLE, "服务暂时不可用，请稍后重试")
}
