# 头像等静态资源路径要存相对路径

- 涉及：`internal/service/chat/server.go` 的 `normalizePath`；前端各页面中 `avatar.startsWith("http")` 的判断
- 来源：基线代码注释“对 SendAvatar 去除前面 /static 之前的所有内容，防止 ip 前缀引入”，结合前端实现整理

## 现象

消息表 `send_avatar` 如果保存 `https://<服务器 IP>:8000/static/avatars/x.png` 这样的完整地址，服务器换 IP、换域名或换端口后，历史消息里的头像全部失效。

## 根因

前端为了方便展示，登录后把 `userInfo.avatar` 拼成了带 `backendUrl` 的完整地址；发送聊天消息时直接把这个值作为 `send_avatar` 上送，地址前缀就被写进了数据库。

## 解决

- 数据库统一保存 `/static/...` 相对路径；前端展示时若不以 `http` 开头再拼接 `backendUrl`。
- 转发服务落库前调用 `normalizePath`，截取 `/static/` 之后的部分；默认头像是外部 CDN 地址，单独放行。

## 遗留问题

- 文件消息的 `url` 仍然保存完整地址，存在同样的问题（TD-10）。
- `normalizePath` 找不到 `/static/` 时只记日志、不返回，随后的切片越界会导致进程崩溃（TD-04）。

## 预防

- 凡是保存服务器自身资源地址的字段，一律存相对路径，由展示层拼接。
- 不要信任客户端上送的展示字段（头像、昵称），更稳妥的做法是落库时按 `send_id` 从用户表读取。
