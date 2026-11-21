package data

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	v1 "github.com/Mars-08042/MarsChat/api/access/v1"
	"github.com/Mars-08042/MarsChat/app/access/internal/biz"
)

// 通话状态的键。三个键总在同一个脚本里一起设置 TTL、一起删除（忙线标记只在仍指向本通话时才删除或续期），
// 所以不会出现通话已经不存在、忙线标记还在的情况；Redis 未配置淘汰策略（noeviction），键不会被单独淘汰。
// 将来改用 Redis Cluster 时，脚本访问的三个键需要用 hash tag 放进同一个槽。
func callKey(id int64) string      { return "access:call:" + strconv.FormatInt(id, 10) }
func callUserKey(uid int64) string { return "access:call_user:" + strconv.FormatInt(uid, 10) }
func callKeys(k biz.CallKey) []string {
	return []string{callKey(k.ID), callUserKey(k.CallerID), callUserKey(k.CalleeID)}
}

var (
	// KEYS: 通话、主叫忙线标记、被叫忙线标记；ARGV: call_id、ttl 毫秒、Hash 的字段与值。
	// 返回 -1 表示通话 ID 已存在，1 表示主叫忙，2 表示被叫忙，0 表示已创建
	createCallScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return -1 end
if redis.call('EXISTS', KEYS[2]) == 1 then return 1 end
if redis.call('EXISTS', KEYS[3]) == 1 then return 2 end
redis.call('HSET', KEYS[1], unpack(ARGV, 3))
redis.call('PEXPIRE', KEYS[1], ARGV[2])
redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
redis.call('SET', KEYS[3], ARGV[1], 'PX', ARGV[2])
return 0
`)

	// KEYS 同上；ARGV: call_id、ttl 毫秒、callee_conn、callee_addr、answered_at。
	// 返回 'accepted'，或者条件不满足时的当前状态（通话不存在时为空串）
	acceptCallScript = redis.NewScript(`
local state = redis.call('HGET', KEYS[1], 'state')
if not state then return '' end
if state ~= 'ringing' then return state end
redis.call('HSET', KEYS[1], 'state', 'active', 'callee_conn', ARGV[3], 'callee_addr', ARGV[4], 'answered_at', ARGV[5])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
for i = 2, 3 do
  if redis.call('GET', KEYS[i]) == ARGV[1] then redis.call('PEXPIRE', KEYS[i], ARGV[2]) end
end
return 'accepted'
`)

	// KEYS 同上；ARGV: call_id、要求相等的字段（空串表示不检查）、字段值、要求的状态（空串表示不检查）。
	// 返回 {'missing'}、{'mismatch', 当前状态} 或 {'ended', 删除前 Hash 的字段与值...}
	endCallScript = redis.NewScript(`
local fields = redis.call('HGETALL', KEYS[1])
if #fields == 0 then return {'missing'} end
local h = {}
for i = 1, #fields, 2 do h[fields[i]] = fields[i + 1] end
if (ARGV[2] ~= '' and h[ARGV[2]] ~= ARGV[3]) or (ARGV[4] ~= '' and h['state'] ~= ARGV[4]) then
  return {'mismatch', h['state']}
end
redis.call('DEL', KEYS[1])
for i = 2, 3 do
  if redis.call('GET', KEYS[i]) == ARGV[1] then redis.call('DEL', KEYS[i]) end
end
table.insert(fields, 1, 'ended')
return fields
`)

	// KEYS 同上；ARGV: call_id、连接 ID、ttl 毫秒。连接是主叫连接或接听连接时返回 1，
	// 处于通话中时同时续期三个键；否则返回 0（包括通话不存在）
	touchCallScript = redis.NewScript(`
local v = redis.call('HMGET', KEYS[1], 'caller_conn', 'callee_conn', 'state')
if v[1] ~= ARGV[2] and v[2] ~= ARGV[2] then return 0 end
if v[3] == 'active' then
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  for i = 2, 3 do
    if redis.call('GET', KEYS[i]) == ARGV[1] then redis.call('PEXPIRE', KEYS[i], ARGV[3]) end
  end
end
return 1
`)
)

// CallRepo 在 Redis 中维护通话状态，见 docs/design/call-signaling.md 第 3.3 节。
type CallRepo struct {
	rdb *redis.Client
}

// NewCallRepo 创建通话状态仓库。
func NewCallRepo(rdb *redis.Client) biz.CallRepo {
	return &CallRepo{rdb: rdb}
}

func (r *CallRepo) Create(ctx context.Context, c *biz.Call, ttl time.Duration) (biz.CreateResult, error) {
	args := []any{c.ID, ttl.Milliseconds(),
		"caller_id", c.CallerID, "caller_conn", c.CallerConn, "caller_addr", c.CallerAddr,
		"callee_id", c.CalleeID, "media", int32(c.Media), "state", string(c.State),
		"created_at", c.CreatedAt.UnixMilli(),
	}
	n, err := createCallScript.Run(ctx, r.rdb, callKeys(c.CallKey), args...).Int()
	if err != nil {
		return 0, fmt.Errorf("create call: %w", err)
	}
	switch n {
	case 0:
		return biz.CreateOK, nil
	case 1:
		return biz.CreateCallerBusy, nil
	case 2:
		return biz.CreateCalleeBusy, nil
	default:
		// 随机 63 位 ID 重复，概率可以忽略，按依赖故障处理
		return 0, fmt.Errorf("create call: id %d already exists", c.ID)
	}
}

func (r *CallRepo) Get(ctx context.Context, id int64) (*biz.Call, error) {
	m, err := r.rdb.HGetAll(ctx, callKey(id)).Result()
	if err != nil {
		return nil, fmt.Errorf("get call: %w", err)
	}
	if len(m) == 0 {
		return nil, nil
	}
	return parseCall(id, m)
}

func (r *CallRepo) Accept(ctx context.Context, k biz.CallKey, connID int64, addr string, at time.Time, ttl time.Duration) (bool, biz.CallState, error) {
	s, err := acceptCallScript.Run(ctx, r.rdb, callKeys(k), k.ID, ttl.Milliseconds(), connID, addr, at.UnixMilli()).Text()
	if err != nil {
		return false, "", fmt.Errorf("accept call: %w", err)
	}
	if s == "accepted" {
		return true, biz.CallActive, nil
	}
	return false, biz.CallState(s), nil
}

func (r *CallRepo) End(ctx context.Context, k biz.CallKey, cond biz.EndCond) (*biz.Call, biz.CallState, error) {
	field, value := "", ""
	switch cond.By {
	case biz.PartyCallerConn:
		field = "caller_conn"
	case biz.PartyCalleeConn:
		field = "callee_conn"
	case biz.PartyCallee:
		field = "callee_id"
	}
	if field != "" {
		value = strconv.FormatInt(cond.ID, 10)
	}
	res, err := endCallScript.Run(ctx, r.rdb, callKeys(k), k.ID, field, value, string(cond.State)).StringSlice()
	if err != nil {
		return nil, "", fmt.Errorf("end call: %w", err)
	}
	switch {
	case len(res) == 0:
		return nil, "", fmt.Errorf("end call: empty reply")
	case res[0] == "missing":
		return nil, "", nil
	case res[0] == "mismatch":
		state := ""
		if len(res) > 1 {
			state = res[1]
		}
		return nil, biz.CallState(state), nil
	}
	m := make(map[string]string, (len(res)-1)/2)
	for i := 1; i+1 < len(res); i += 2 {
		m[res[i]] = res[i+1]
	}
	c, err := parseCall(k.ID, m)
	if err != nil {
		return nil, "", err
	}
	return c, "", nil
}

func (r *CallRepo) Touch(ctx context.Context, k biz.CallKey, connID int64, ttl time.Duration) (bool, error) {
	n, err := touchCallScript.Run(ctx, r.rdb, callKeys(k), k.ID, connID, ttl.Milliseconds()).Int()
	if err != nil {
		return false, fmt.Errorf("touch call: %w", err)
	}
	return n == 1, nil
}

// parseCall 解析 Hash。数字字段缺失时为 0（例如振铃中没有 callee_conn），格式错误时报错。
func parseCall(id int64, m map[string]string) (*biz.Call, error) {
	var perr error
	num := func(f string) int64 {
		s, ok := m[f]
		if !ok || perr != nil {
			return 0
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			perr = fmt.Errorf("parse call %d field %s=%q: %w", id, f, s, err)
		}
		return n
	}
	c := &biz.Call{
		CallKey:    biz.CallKey{ID: id, CallerID: num("caller_id"), CalleeID: num("callee_id")},
		CallerConn: num("caller_conn"),
		CallerAddr: m["caller_addr"],
		CalleeConn: num("callee_conn"),
		CalleeAddr: m["callee_addr"],
		Media:      v1.CallMedia(num("media")),
		State:      biz.CallState(m["state"]),
		CreatedAt:  time.UnixMilli(num("created_at")),
	}
	if at := num("answered_at"); at != 0 {
		c.AnsweredAt = time.UnixMilli(at)
	}
	if perr != nil {
		return nil, perr
	}
	return c, nil
}
