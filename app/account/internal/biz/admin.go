package biz

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Mars-08042/MarsChat/api/account/v1"
	"github.com/Mars-08042/MarsChat/pkg/auth"
)

// AdminUsecase 负责管理员的封禁与解封。
type AdminUsecase struct {
	users    UserRepo
	sessions SessionRepo
	events   EventRepo
	tx       Transaction
	tokens   *TokenService
	revoker  Revoker
	logger   *slog.Logger
	now      func() time.Time
}

// NewAdminUsecase 创建管理用例。
func NewAdminUsecase(users UserRepo, sessions SessionRepo, events EventRepo, tx Transaction, tokens *TokenService, revoker Revoker, logger *slog.Logger) *AdminUsecase {
	return &AdminUsecase{
		users: users, sessions: sessions, events: events, tx: tx, tokens: tokens, revoker: revoker, logger: logger,
		now: nowUTC,
	}
}

// Ban 封禁用户：修改状态、吊销全部会话，并让已签发的访问令牌立即失效。重复封禁是幂等的。
func (uc *AdminUsecase) Ban(ctx context.Context, admin auth.Principal, userID int64) error {
	if err := uc.check(admin, userID); err != nil {
		return err
	}
	now := uc.now()
	var changed bool
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if changed, err = uc.setStatus(ctx, userID, StatusBanned, now); err != nil || !changed {
			return err
		}
		// 不逐个写 SessionRevoked：一条 UserBanned 足以让 access 断开该用户的全部连接
		if _, err := uc.sessions.RevokeAllByUser(ctx, userID, ReasonBanned, now); err != nil {
			return err
		}
		return uc.events.Add(ctx, TopicAccountEvents, strconv.FormatInt(userID, 10), &v1.UserBanned{
			UserId:   userID,
			BannedAt: timestamppb.New(now),
		}, now)
	})
	if err != nil {
		return err
	}
	// 用提交之后的时间：与封禁并发的登录、刷新都在封禁提交前签发令牌（见 AuthUsecase.login），
	// 它们的 iat 不会晚于这个时间。重复封禁也重写一次，用于修复上次提交后写入失败的情况
	if err := uc.revoker.RevokeUser(context.WithoutCancel(ctx), userID, uc.now(), uc.tokens.RevocationTTL()); err != nil {
		uc.logger.Error("write user revocation failed", slog.Int64("user_id", userID), slog.Any("error", err))
	}
	uc.users.EvictCache(ctx, userID)
	if changed {
		uc.logger.Info("user banned", slog.Int64("user_id", userID), slog.Int64("admin_id", admin.UserID))
	}
	return nil
}

// Unban 解封用户。用户级吊销标记不删除：它只拒绝封禁前签发的令牌，解封后重新登录不受影响。
func (uc *AdminUsecase) Unban(ctx context.Context, admin auth.Principal, userID int64) error {
	if err := uc.check(admin, userID); err != nil {
		return err
	}
	changed, err := uc.setStatus(ctx, userID, StatusNormal, uc.now())
	if err != nil || !changed {
		return err
	}
	uc.users.EvictCache(ctx, userID)
	uc.logger.Info("user unbanned", slog.Int64("user_id", userID), slog.Int64("admin_id", admin.UserID))
	return nil
}

const (
	defaultUserPageSize = 20
	maxUserPageSize     = 50
)

// UserPage 是用户列表的一页。Total 是满足筛选条件的总数，与游标无关。
type UserPage struct {
	Users      []*User
	NextCursor int64
	Total      int64
}

// List 按注册时间倒序（ID 倒序）列出用户。直接读库，不经资料缓存，封禁、解封后立即可见。
// 按昵称包含匹配用不上索引，只能扫表；管理员低频使用，当前规模可以接受（设计 9.2）。
// 邮箱入库时已转为小写，users 表的排序规则也不区分大小写，匹配都不区分大小写。
func (uc *AdminUsecase) List(ctx context.Context, admin auth.Principal, keyword string, status int32, cursor int64, limit int32) (*UserPage, error) {
	if !admin.IsAdmin() {
		return nil, ErrPermissionDenied()
	}
	f, ok := parseUserKeyword(keyword)
	if !ok {
		return &UserPage{}, nil
	}
	f.Status = status
	n := int(limit)
	switch {
	case n <= 0:
		n = defaultUserPageSize
	case n > maxUserPageSize:
		n = maxUserPageSize
	}
	// 多查一条判断是否还有下一页
	users, err := uc.users.List(ctx, f, cursor, n+1)
	if err != nil {
		return nil, err
	}
	total, err := uc.users.Count(ctx, f)
	if err != nil {
		return nil, err
	}
	page := &UserPage{Users: users, Total: total}
	if len(users) > n {
		page.Users = users[:n]
		page.NextCursor = users[n-1].ID
	}
	return page, nil
}

// parseUserKeyword 把关键词转成筛选条件：全是数字按 ID 精确匹配，含 @ 按邮箱前缀匹配，
// 其他按昵称包含或邮箱前缀匹配（昵称包含已经要扫表，顺带匹配邮箱不增加代价）。
// 数字超出 int64 时不可能有这个用户，返回 false。
func parseUserKeyword(keyword string) (UserFilter, bool) {
	k := strings.TrimSpace(keyword)
	switch {
	case k == "":
		return UserFilter{}, true
	case isDigits(k):
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil || id == 0 {
			return UserFilter{}, false
		}
		return UserFilter{ID: id}, true
	case strings.Contains(k, "@"):
		// 邮箱入库时统一转为小写
		return UserFilter{EmailPrefix: strings.ToLower(k)}, true
	default:
		return UserFilter{Text: k}, true
	}
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (uc *AdminUsecase) check(admin auth.Principal, userID int64) error {
	if !admin.IsAdmin() {
		return ErrPermissionDenied()
	}
	if admin.UserID == userID {
		// 防止管理员误操作把自己锁在外面
		return ErrPermissionDenied()
	}
	return nil
}

// setStatus 修改状态；用户不存在时返回 USER_NOT_FOUND，状态本来就是目标值时返回 false。
func (uc *AdminUsecase) setStatus(ctx context.Context, userID int64, status int32, now time.Time) (bool, error) {
	changed, err := uc.users.SetStatus(ctx, userID, status, now)
	if err != nil || changed {
		return changed, err
	}
	if _, err := uc.users.FindByID(ctx, userID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, ErrUserNotFound()
		}
		return false, err
	}
	return false, nil
}
