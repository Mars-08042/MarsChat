package auth

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// 吊销名单的键由所有服务共享，所以统一定义在这里。
const (
	revokedSessionPrefix = "auth:revoked:sid:"
	revokedUserPrefix    = "auth:revoked:user:"
)

// Revocations 读写 Redis 中的吊销名单。
//   - 会话级：退出、踢设备后，该会话的访问令牌全部失效；
//   - 用户级：值为封禁时间（Unix 秒），签发时间不晚于它的令牌全部失效。解封时不删除，
//     这样封禁前签发、尚未过期的令牌不会复活；重新登录签发的令牌时间更晚，不受影响。
//
// 记录的 TTL 必须不小于访问令牌有效期，否则令牌会在记录过期后重新生效。
type Revocations struct {
	rdb redis.UniversalClient
}

// NewRevocations 创建吊销名单访问器。
func NewRevocations(rdb redis.UniversalClient) *Revocations {
	return &Revocations{rdb: rdb}
}

// RevokeSessions 吊销会话。
func (r *Revocations) RevokeSessions(ctx context.Context, ttl time.Duration, sessionIDs ...int64) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	pipe := r.rdb.Pipeline()
	for _, sid := range sessionIDs {
		pipe.Set(ctx, revokedSessionPrefix+strconv.FormatInt(sid, 10), 1, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

// RevokeUser 让用户在 at 及之前签发的令牌全部失效。
func (r *Revocations) RevokeUser(ctx context.Context, userID int64, at time.Time, ttl time.Duration) error {
	key := revokedUserPrefix + strconv.FormatInt(userID, 10)
	if err := r.rdb.Set(ctx, key, at.Unix(), ttl).Err(); err != nil {
		return fmt.Errorf("revoke user %d: %w", userID, err)
	}
	return nil
}

// IsRevoked 用一次 MGET 同时检查会话级和用户级记录。
func (r *Revocations) IsRevoked(ctx context.Context, p Principal) (bool, error) {
	vals, err := r.rdb.MGet(ctx,
		revokedSessionPrefix+strconv.FormatInt(p.SessionID, 10),
		revokedUserPrefix+strconv.FormatInt(p.UserID, 10),
	).Result()
	if err != nil {
		return false, fmt.Errorf("check revocation: %w", err)
	}
	if vals[0] != nil {
		return true, nil
	}
	if s, ok := vals[1].(string); ok {
		revokedAt, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return false, fmt.Errorf("parse user revocation %q: %w", s, err)
		}
		// JWT 的 iat 精确到秒，同一秒内签发的也视为已吊销，宁可多拒绝
		if p.IssuedAt.Unix() <= revokedAt {
			return true, nil
		}
	}
	return false, nil
}
