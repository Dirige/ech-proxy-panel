# Getting Started

## 前置

- 服务端地址（形如 `xxx.workers.dev:443`）+ 令牌（如果服务端要求）
- 以下三选一：Docker、或 `ech-workers` 二进制、或 `ECHWorkersGUI.exe`（二进制/镜像在外部，不在本目录）

## 9 个字段（默认值依据上游 `ech-workers.go:217-228`）

| 字段 | 必填 | 默认值（flag） | 说明 |
|------|------|---------------|------|
| `listen_addr` | 否 | `127.0.0.1:30000` | 本地 SOCKS5/HTTP 监听。本包用 `0.0.0.0:30000` 以便局域网/容器访问 |
| `server_addr` | 是 | 空 | 服务端地址，必须带 `:443` |
| `server_ip` | 否 | 空 | 优选 IP 或优选域名，绕过 DNS；留空自动分配 |
| `token` | 视服务端 | 空 | 身份验证令牌 |
| `dns_server` | 否 | `dns.alidns.com/dns-query` | ECH 查询用 DoH |
| `ech_domain` | 否 | `cloudflare-ech.com` | ECH 查询域名 |
| `routing_mode` | 否 | `global` | `global` 全局代理 / `bypass_cn` 跳过大陆 / `none` 直连 / `custom` 自定义（需 `-rules` 文件） |
| `web_addr` | 否 | 空 | 面板监听，如 `:9090`；留空则不启动面板 |
| `proxy_ip` | 否 | 空 | 固定出口，`IP` 或 `IP:端口`；留空自动分配 |

## 启动方式

### A. Docker（软路由推荐，运行目录：任意）

```bash
docker run -d --name ech-proxy --restart always -p 9090:9090 -p 30000:30000 -v ./config.json:/data/config.json ghcr.io/dirige/ech-proxy-panel:latest
docker logs -f ech-proxy
```

或用本目录的 `docker-compose.yml`（运行目录：ech-wk-main，先从 `config.example.json` 复制出 `config.json`）：

```bash
docker compose up -d
docker logs -f ech-proxy
```

面板：`http://你的IP:9090`。改配置可在面板「配置」页保存，或 `docker exec -it ech-proxy vi /data/config.json` 后 `docker restart ech-proxy`。

### B. 命令行二进制（运行目录：二进制所在目录）

```powershell
.\ech-workers.exe -config ./config.json
# 或直接传参（会回写 config.json，见上游 ech-workers.go:1084-1093）
.\ech-workers.exe -l 0.0.0.0:30000 -f 你的服务地址:443 -token your-token -web :9090 -routing bypass_cn
```

### C. 桌面 GUI（Windows）

1. 把 `config.json` 放到 `ECHWorkersGUI.exe` 同目录（或在 GUI 里逐项填）。
2. 双击 `ECHWorkersGUI.exe` 启动。
3. 浏览器/系统代理指向 `127.0.0.1:30000` 验证。

## 自定义分流（`custom` 才看）

规则文件每行一条（上游 README 原样）：

```
domain,baidu.com,direct
domain,google.com,proxy
```

`domain` 为包含匹配。Docker 挂载示例（运行目录：任意）：

```bash
docker run -d --name ech-proxy --restart always -p 9090:9090 -p 30000:30000 -v ech-data:/data -v /path/to/rules.txt:/etc/rules.txt ghcr.io/dirige/ech-proxy-panel:latest -l 0.0.0.0:30000 -f 你的服务地址:443 -token 你的令牌 -web :9090 -routing custom -rules /etc/rules.txt
```

本目录提供 `rules.example.txt` 模板：复制为 `rules.txt` 后挂载（`docker-compose.yml` 里已留好挂载注释行）。
