#!/bin/sh
# 只在 MySQL 数据卷首次初始化时由官方镜像执行。
# 每个服务一个库、一个只能访问本库的账号，服务之间不能跨库读写。
# 新增服务库时同时修改本脚本；已有数据卷不会重新执行，需要手动建库或重建数据卷。
set -eu

create_service_db() {
	db="$1"
	user="$2"
	password="$3"
	mysql -uroot -p"$MYSQL_ROOT_PASSWORD" <<SQL
CREATE DATABASE IF NOT EXISTS \`$db\` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS '$user'@'%' IDENTIFIED BY '$password';
GRANT ALL PRIVILEGES ON \`$db\`.* TO '$user'@'%';
SQL
}

create_service_db marschat_account account "$MARSCHAT_ACCOUNT_DB_PASSWORD"
create_service_db marschat_social social "$MARSCHAT_SOCIAL_DB_PASSWORD"
create_service_db marschat_message message "$MARSCHAT_MESSAGE_DB_PASSWORD"
create_service_db marschat_media media "$MARSCHAT_MEDIA_DB_PASSWORD"
