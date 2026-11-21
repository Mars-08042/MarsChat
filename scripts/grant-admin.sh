#!/bin/sh
# 把已注册的用户设为管理员（系统不提供设置管理员的接口）。用法：make grant-admin EMAIL=someone@example.com
# 修改数据库后删除该用户的资料缓存；新角色在该用户下次刷新令牌或重新登录后生效。
set -eu

# 与服务端一致：按小写匹配
email=$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')
case "$email" in
'' | *[!a-z0-9@._+-]* | *@*@* | @* | *@)
	echo "usage: make grant-admin EMAIL=<email>" >&2
	exit 1
	;;
esac
case "$email" in
*@*) ;;
*)
	echo "usage: make grant-admin EMAIL=<email>" >&2
	exit 1
	;;
esac

# 邮箱已限制为字母、数字和 @._+-，不含引号，可以直接拼进 SQL
id=$(docker compose exec -T mysql sh -c \
	"mysql -uroot -p\"\$MYSQL_ROOT_PASSWORD\" -N -B marschat_account -e \"UPDATE users SET role = 2, updated_at = UTC_TIMESTAMP(3) WHERE email = '$email'; SELECT id FROM users WHERE email = '$email'\"" 2>/dev/null)

if [ -z "$id" ]; then
	echo "user with email $email not found" >&2
	exit 1
fi

docker compose exec -T redis sh -c "redis-cli -a \"\$REDIS_PASSWORD\" --no-auth-warning DEL account:user:v2:$id" >/dev/null
echo "user $id is now an admin; the role takes effect after the next token refresh or login"
