# ech-wk-main

一句话：ECH Workers 代理客户端的**运行配置包**——改好 `config.json`，交给外部二进制/Docker 去跑。

定位：本包是原 ECH Proxy Panel 项目的**便捷分流策略补充方案**（一套可直接套用的 `bypass_cn` 配置），原项目为主、本包为辅；二进制/面板/源码均在原项目。

> 上游说明：`../ech-proxy-panel-trae-agent-XS7r7t/README.md`（参数全集+面板）、`../ECHWorkers-windows-amd64/README.txt`（桌面端）。本目录只管配置，不管二进制。

## 功能特性

- 开箱即用的 9 字段 `config.json`（监听 / 落地 / 分流 / 面板全覆盖）
- `bypass_cn` 分流：国内直连、国外代理，软路由友好
- 优选 IP/域名 + 固定出口 IP 支持
- Web 面板（`:9090`）可视化改配置、看流量
- `config.example.json` 模板保留（包内 token 本就公开，分享无压力）
- `docker-compose.yml` 一键启动 + `rules.example.txt` 自定义分流模板 + `Test-Config.ps1` 一键校验

## 快速开始

前置：已拿到服务端地址+令牌；已准备 `ech-workers` 二进制或 Docker。

```powershell
# 1. 进目录（运行目录：任意，先进到本目录所在位置）
Set-Location -LiteralPath "./ech-wk-main"

# 2. 复制模板改出自己的配置
Copy-Item -LiteralPath "config.example.json" -Destination "config.json"

# 3. 一键校验
.\scripts\Test-Config.ps1

# 4a. Docker 跑（运行目录：ech-wk-main）
docker compose up -d
# 备选：单条 docker run（运行目录：任意）
# docker run -d --name ech-proxy --restart always -p 9090:9090 -p 30000:30000 -v ${PWD}/config.json:/data/config.json ghcr.io/dirige/ech-proxy-panel:latest

# 4b. 桌面端跑：把 config.json 放到 ECHWorkersGUI.exe 同目录，双击 GUI 填入即可（详见 docs/getting-started.md）
```

跑起来后：浏览器开 `http://你的IP:9090` 进面板；本地代理 `127.0.0.1:30000`（SOCKS5/HTTP）。

## 配置

完整 9 字段说明见 `docs/getting-started.md`。速查：

| 字段 | 作用 | 示例（占位） |
|------|------|--------------|
| `listen_addr` | 本地监听 | `0.0.0.0:30000` |
| `server_addr` | 服务端地址 | `你的服务地址:443` |
| `server_ip` | 优选 IP/域名，可空 | `172.64.229.240` |
| `token` | 认证令牌 | `your-token` |
| `dns_server` | DoH | `dns.alidns.com/dns-query` |
| `ech_domain` | ECH 域名 | `cloudflare-ech.com` |
| `routing_mode` | 分流：`global`/`bypass_cn`/`none`/`custom` | `bypass_cn` |
| `web_addr` | 面板监听 | `:9090` |
| `proxy_ip` | 固定出口，可空 | `101.79.165.113:443` |

> 本目录现役 `config.json` 用的是 `bypass_cn`；面板模板默认 `global`。要全局代理就改 `global`。

## 使用文档

- `docs/index.md` — 文档入口
- `docs/getting-started.md` — 9 字段详解+三种启动方式
- `docs/faq-troubleshooting.md` — 排错
- `AGENTS.md` — 给 AI 看的仓库约定

## 说明与安全提示

- 本包 `token`/`server_addr` 系公开群组的公开信息，**无需保密**：`config.json` 可正常提交、截图分享；`config.example.json` 仅作模板保留。
- `web_addr` 默认 `:9090` 监听全接口且面板无登录鉴权：公网机必须收敛到 `127.0.0.1:9090` 或加反代鉴权 + 防火墙收紧；仅内网软路由场景才保留全接口监听。

## 相关项目

- 原项目：[byJoey/ech-wk](https://github.com/byJoey/ech-wk)
- 面板二次开发版：`../ech-proxy-panel-trae-agent-XS7r7t/`（本地只读参考）
- 许可证：本目录见 `LICENSE`（MIT）；上游代码以其仓库 LICENSE 为准。
