package data

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mars-08042/MarsChat/app/account/internal/biz"
)

// 每日计数键名带日期，自然换天；TTL 只用于清理，取 25 小时覆盖时区边界
const smsDailyTTL = 25 * time.Hour

// acquireSlotScript 原子地检查冷却和当日上限，通过时设置冷却并计数。
// 返回 {结果, 剩余冷却毫秒}：0 通过、1 冷却中、2 达到当日上限。
var acquireSlotScript = redis.NewScript(`
local pttl = redis.call('PTTL', KEYS[1])
if pttl == -1 then pttl = tonumber(ARGV[1]) end
if pttl > 0 then return {1, pttl} end
local n = tonumber(redis.call('GET', KEYS[2]) or '0')
if n >= tonumber(ARGV[2]) then return {2, 0} end
redis.call('SET', KEYS[1], '1', 'PX', ARGV[1])
if redis.call('INCR', KEYS[2]) == 1 then redis.call('PEXPIRE', KEYS[2], ARGV[3]) end
return {0, 0}`)

// releaseSlotScript 撤销一次占用：删除冷却，计数大于 0 时减一。
var releaseSlotScript = redis.NewScript(`
redis.call('DEL', KEYS[1])
local n = tonumber(redis.call('GET', KEYS[2]) or '0')
if n > 0 then redis.call('DECR', KEYS[2]) end
return 0`)

// verifyCodeScript 比对验证码哈希。返回值与 biz.VerifyResult 一致：
// 0 正确（删除）、1 不存在或已过期、2 错误、3 错误次数达到上限（删除）。
var verifyCodeScript = redis.NewScript(`
local h = redis.call('HGET', KEYS[1], 'h')
if not h then return 1 end
if h == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return 0
end
if redis.call('HINCRBY', KEYS[1], 'n', 1) >= tonumber(ARGV[2]) then
  redis.call('DEL', KEYS[1])
  return 3
end
return 2`)

type smsCodeStore struct{ rdb *redis.Client }

// NewSmsCodeStore 创建验证码存储。
func NewSmsCodeStore(rdb *redis.Client) biz.SmsCodeStore {
	return &smsCodeStore{rdb: rdb}
}

func smsCodeKey(purpose int32, phone string) string {
	return "account:sms:code:" + strconv.Itoa(int(purpose)) + ":" + phone
}

func smsCooldownKey(phone string) string { return "account:sms:cooldown:" + phone }

func smsDailyKey(phone, day string) string { return "account:sms:daily:" + phone + ":" + day }

func (s *smsCodeStore) AcquireSendSlot(ctx context.Context, phone, day string, interval time.Duration, dailyLimit int) (biz.SlotResult, time.Duration, error) {
	res, err := acquireSlotScript.Run(ctx, s.rdb,
		[]string{smsCooldownKey(phone), smsDailyKey(phone, day)},
		interval.Milliseconds(), dailyLimit, smsDailyTTL.Milliseconds()).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	return biz.SlotResult(res[0]), time.Duration(res[1]) * time.Millisecond, nil
}

func (s *smsCodeStore) ReleaseSendSlot(ctx context.Context, phone, day string) error {
	return releaseSlotScript.Run(ctx, s.rdb, []string{smsCooldownKey(phone), smsDailyKey(phone, day)}).Err()
}

// SaveCode 覆盖旧验证码，错误次数随之清零；重新获取受发送间隔限制，不会被用来绕过次数上限。
func (s *smsCodeStore) SaveCode(ctx context.Context, purpose int32, phone, codeHash string, ttl time.Duration) error {
	key := smsCodeKey(purpose, phone)
	_, err := s.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Del(ctx, key)
		pipe.HSet(ctx, key, "h", codeHash, "n", 0)
		pipe.PExpire(ctx, key, ttl)
		return nil
	})
	return err
}

func (s *smsCodeStore) DeleteCode(ctx context.Context, purpose int32, phone string) error {
	return s.rdb.Del(ctx, smsCodeKey(purpose, phone)).Err()
}

func (s *smsCodeStore) VerifyCode(ctx context.Context, purpose int32, phone, codeHash string, maxAttempts int) (biz.VerifyResult, error) {
	n, err := verifyCodeScript.Run(ctx, s.rdb, []string{smsCodeKey(purpose, phone)}, codeHash, maxAttempts).Int64()
	if err != nil {
		return 0, err
	}
	return biz.VerifyResult(n), nil
}
