// Package route 定义在线路由的 Redis 结构：access 在连接认证和每次心跳时写入，delivery 推送前读取。
//
// 每个用户一个 Hash access:route:{user_id}，字段为连接 ID，值为 "{session_id}|{access gRPC 地址}"。
// 每个字段单独设置 TTL（HSETEX，需要 Redis 8.0+）：一台设备崩溃后，即使该用户的其他设备还在续期，
// 崩溃设备的字段也会按时过期。整键 TTL 做不到这一点。
package route

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Entry 是一条连接的路由。
type Entry struct {
	ConnID    int64
	SessionID int64
	// access 实例的 gRPC 地址（host:port）
	Addr string
}

// Key 返回用户的路由键。
func Key(userID int64) string {
	return "access:route:" + strconv.FormatInt(userID, 10)
}

func encode(e Entry) string {
	return strconv.FormatInt(e.SessionID, 10) + "|" + e.Addr
}

func decode(field, value string) (Entry, error) {
	connID, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("invalid route field %q", field)
	}
	sid, addr, ok := strings.Cut(value, "|")
	if !ok || addr == "" {
		return Entry{}, fmt.Errorf("invalid route value %q", value)
	}
	sessionID, err := strconv.ParseInt(sid, 10, 64)
	if err != nil {
		return Entry{}, fmt.Errorf("invalid route value %q", value)
	}
	return Entry{ConnID: connID, SessionID: sessionID, Addr: addr}, nil
}

// Register 登记或续期一条路由，ttl 后该字段自动删除。重复执行是幂等的，Redis 重启丢失路由后下次心跳即可恢复。
func Register(ctx context.Context, rdb redis.Cmdable, userID int64, e Entry, ttl time.Duration) error {
	return rdb.HSetEXWithArgs(ctx, Key(userID), &redis.HSetEXOptions{
		ExpirationType: redis.HSetEXExpirationPX,
		ExpirationVal:  ttl.Milliseconds(),
	}, strconv.FormatInt(e.ConnID, 10), encode(e)).Err()
}

// Unregister 删除一条路由。连接 ID 每条连接独有，直接删除不会误删其他连接的路由。
func Unregister(ctx context.Context, rdb redis.Cmdable, userID, connID int64) error {
	return rdb.HDel(ctx, Key(userID), strconv.FormatInt(connID, 10)).Err()
}

// List 查询多个用户的在线连接，用 pipeline 合并为一次往返。不在线的用户不出现在结果中。
// 无法解析的字段被跳过并返回第一个解析错误，调用方可以记日志后继续使用结果。
func List(ctx context.Context, rdb redis.Cmdable, userIDs []int64) (map[int64][]Entry, error) {
	if len(userIDs) == 0 {
		return map[int64][]Entry{}, nil
	}
	cmds := make([]*redis.MapStringStringCmd, len(userIDs))
	_, err := rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, uid := range userIDs {
			cmds[i] = p.HGetAll(ctx, Key(uid))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]Entry, len(userIDs))
	var firstErr error
	for i, uid := range userIDs {
		for field, value := range cmds[i].Val() {
			e, err := decode(field, value)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			out[uid] = append(out[uid], e)
		}
	}
	return out, firstErr
}
