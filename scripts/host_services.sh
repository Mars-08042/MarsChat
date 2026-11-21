#!/bin/sh
# 在宿主机后台运行全部六个服务，供 make e2e 使用；二进制与日志写入被忽略的 tmp/。
# 用法：sh scripts/host_services.sh start | wait | status | stop
#   start   构建并启动，进程号与日志在 tmp/hostlogs/
#   wait    等待六个服务的 /readyz 返回 200（最多 60 秒），并检查进程是否存活
#   status  打印各服务 /readyz 的状态码
#   stop    结束 start 启动的进程
# 邮件只在本进程环境中改为 log 发送方与固定验证码 000000，不修改 .env。
# 不要和容器模式（make cluster-up）同时运行：端口和 Consul 服务名相同。
# Windows 上 kill 会强制结束进程，服务来不及从 Consul 注销；TTL 心跳停止后实例变为 critical，不再被发现。
set -e
cd "$(dirname "$0")/.."
SERVICES="account social message delivery access media"
BIN=tmp/hostbin
LOGDIR=tmp/hostlogs

case "$1" in
start)
  test -f .env || { echo "missing .env: copy .env.example to .env and set the passwords"; exit 1; }
  mkdir -p "$BIN" "$LOGDIR"
  go build -o "$BIN/" $(for s in $SERVICES; do printf './app/%s/cmd/%s ' "$s" "$s"; done)
  set -a; . ./.env; set +a
  export MARSCHAT_MAIL_PROVIDER=log MARSCHAT_MAIL_FIXED_CODE=000000
  for s in $SERVICES; do
    "./$BIN/$s" -conf "app/$s/configs" >"$LOGDIR/$s.log" 2>&1 &
    echo $! >"$LOGDIR/$s.pid"
    echo "started $s pid $!"
  done
  ;;
stop)
  for s in $SERVICES; do
    if [ -f "$LOGDIR/$s.pid" ]; then
      pid=$(cat "$LOGDIR/$s.pid")
      kill "$pid" 2>/dev/null && echo "stopped $s pid $pid" || echo "$s pid $pid not running"
      rm -f "$LOGDIR/$s.pid"
    fi
  done
  ;;
status)
  for p in 18000 18010 18020 18030 18040 18050; do
    code=$(curl -s -o /dev/null -w '%{http_code}' --noproxy '*' "http://127.0.0.1:$p/readyz" || true)
    echo "port $p readyz $code"
  done
  ;;
wait)
  i=0
  while [ $i -lt 60 ]; do
    n=$(sh "$0" status | grep -c "readyz 200" || true)
    [ "$n" -eq 6 ] && break
    i=$((i + 1)); sleep 1
  done
  sh "$0" status
  for s in $SERVICES; do
    pid=$(cat "$LOGDIR/$s.pid")
    if kill -0 "$pid" 2>/dev/null; then echo "$s alive"; else echo "$s DEAD"; tail -5 "$LOGDIR/$s.log"; fi
  done
  ;;
*)
  echo "usage: $0 start|wait|status|stop"; exit 2 ;;
esac
