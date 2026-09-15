# FAQ / 排错

## 连不上服务端

1. 先看 `server_addr` 是否带 `:443`。
2. `server_ip` 若填了优选域名，先验证：`nslookup youxuan.example.com`（换成你的值），不通就清空让它自动分配。
3. 切 DoH 试试：`dns_server` 换 `1.1.1.1/dns-query` 或 `8.8.8.8/dns-query`。
4. 看日志：`docker logs --tail 200 ech-proxy`。

## 端口占用（30000 / 9090）

```powershell
# 查谁占了 30000（运行目录：任意）
netstat -ano | Select-String "30000"
# 换端口：改 config.json 里 listen_addr / web_addr 后重启
```

`listen_addr` 与 `web_addr` 不能是同一个端口。

## 面板打不开

- 安全先行：面板无登录鉴权，`web_addr=":9090"` 全接口监听只适合内网；公网机先收敛到 `127.0.0.1:9090` 再排查。
- Docker 没映射 `-p 9090:9090` 最常见。
- `web_addr` 留空 = 面板没启动，填 `:9090` 后重启。
- 软路由/爱快用桥接模式时，用宿主机 IP+9090 访问，不是容器内 IP。

## 分流不生效

- `bypass_cn` 依赖内置/离线中国 IP 表（桌面端 `chn_ip.txt`）；Docker 版行为以上游镜像为准。
- `custom` 必须配 `-rules` 文件并挂载，否则 fallback 不可预期。
- 改完 `routing_mode` 必须重启进程/容器。

## token 对不上

- 服务端没设 token 时客户端留空即可；设了就必须一致。
- 本包 token 系公开群组公开信息，无需保密，可直接分享 `config.json`。
