# 单文件挂载的网关配置改了不生效，也不报错

- 涉及：[compose.yaml](../../compose.yaml) 中 APISIX 的 `./deploy/apisix/apisix.yaml:/usr/local/apisix/conf/apisix.yaml:ro`
- 发现时间：2026-11-22，前端第 9 阶段给网关加前端路由时

## 现象

给 `apisix.yaml` 加了前端路由，并把证书改成 `sni: "*"`。文档写的是保存后约 1 秒内热更新，但 `/` 一直返回 `404 Route Not Found`，用自定义域名访问 HTTPS 时握手失败，日志是 `failed to match any SSL certificate by SNI`。网关日志里没有任何加载失败的错误，原有路由照常工作。

## 根因

容器里的文件不是宿主机上的新文件：

- `docker exec` 进去看，`apisix.yaml` 的修改时间还是两天前，内容里有第一处修改（新上游），却没有后面的修改，末尾停在半个单词，没有 `#END`；
- 编辑工具保存时先写一个新文件再改名替换原文件。单文件挂载绑定的是容器启动时的那个文件，替换之后容器看到的仍是旧文件；Docker Desktop 的文件共享在这种情况下还会给出新旧混合的内容（大小按旧文件、内容部分来自新文件）；
- APISIX 只在文件以 `#END` 结尾时才加载，没有这一行就认为文件还没写完，保留旧配置，不打印错误。所以表现为改了不生效、也没有报错。

之前的热更新能生效，是因为当时的编辑方式是原地写入同一个文件。

## 解决

重建网关容器，重新挂载：

```bash
docker compose --profile apps up -d --no-deps --force-recreate apisix
```

之后容器里的文件末尾是 `#END`，新路由和证书配置都生效。

## 预防

- 改完 `apisix.yaml` 后确认容器里的文件末尾是 `#END`：`docker compose exec apisix tail -1 /usr/local/apisix/conf/apisix.yaml`；不是就按上面的命令重建网关容器。已写入 [deployment.md](../deployment.md#网关)。
- 同样是单文件挂载的还有 `deploy/apisix/config.yaml`、`deploy/coturn/turnserver.conf`，它们本来就要重启容器才生效，重建容器即可。
- 需要稳定热更新的配置优先挂载目录而不是单个文件：目录挂载时改名替换也能看到新文件。APISIX 的 `apisix.yaml` 路径固定在 `conf/` 目录里，和其他配置在一起，所以暂时保留单文件挂载。
