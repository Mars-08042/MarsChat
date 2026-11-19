#!/bin/sh
# 把已注册的用户设为管理员（系统不提供设置管理员的接口）。用法：make grant-admin PHONE=13800000000
# 修改数据库后删除该用户的资料缓存；新角色在该用户下次刷新令牌或重新登录后生效。
set -eu

phone="${1:-}"
case "$phone" in
'' | *[!0-9]*)
	echo "usage: make grant-admin PHONE=<11-digit phone>" >&2
	exit 1
	;;
esac

# 手机号已校验为纯数字，可以直接拼进 SQL
id=$(docker compose exec -T mysql sh -c \
	"mysql -uroot -p\"\$MYSQL_ROOT_PASSWORD\" -N -B marschat_account -e \"UPDATE users SET role = 2, updated_at = UTC_TIMESTAMP(3) WHERE phone = '$phone'; SELECT id FROM users WHERE phone = '$phone'\"" 2>/dev/null)

if [ -z "$id" ]; then
	echo "user with phone $phone not found" >&2
	exit 1
fi

docker compose exec -T redis sh -c "redis-cli -a \"\$REDIS_PASSWORD\" --no-auth-warning DEL account:user:$id" >/dev/null
echo "user $id is now an admin; the role takes effect after the next token refresh or login"
