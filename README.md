# ECH Proxy Panel

基于 [byJoey/ech-wk](https://github.com/byJoey/ech-wk) 二次修改，增加了 Web 管理面板和部分功能。

> 免费 ECH 节点由 [Telegram 频道](https://t.me/honghongtg) 提供，感谢维护！
>
> 本项目仅在原项目基础上补充了 Web 面板、流量统计、分流规则、出口检测等客户端功能，Cloudflare Worker 服务端保持不动。

## 功能

- **SOCKS5 + HTTP 代理**：统一监听，自动识别
- **分流模式**：`bypass_cn`（默认，跳过中国大陆）/ `global` / `none` / `custom`
- **自定义规则**：Web 面板增删直连/代理域名，支持 `*.example.com` 通配符和子域名匹配
- **Web 管理面板**：暗色主题，概览/连接/规则/配置四个页面
- **实时监控**：上传下载速度、总流量、活跃连接、内存、出口 IP/COLO、延迟
- **登录鉴权**：可选密码保护（不设密码则无需登录）
- **配置持久化**：`/data/config.json` + `/data/rules.json`，重启不丢失
- **中国 IP 列表**：内置 CIDR 格式快照，启动后经 ECH 隧道后台自动更新

## 快速开始

### Docker（推荐）

```bash
docker run -d --name ech --restart always \
  -p 30000:30000 -p 30001:9090 \
  -v ./data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest
```

首次运行使用内置默认配置（`hhech.nb1tap.kdns.fr:443` / `honghongfree`），打开 `http://你的IP:30001` 管理。

### Docker Compose

```bash
docker compose up -d
```

### 自定义参数

**方式一：命令行参数**

```bash
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 9091:9090 \
  -v ech-data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest \
  -f 你的服务地址:443 \
  -token 你的令牌 \
  -web :9090 \
  -routing bypass_cn \
  -password 你的管理密码
```

**方式二：环境变量**

```bash
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 9091:9090 \
  -v ech-data:/data \
  -e ECH_SERVER=你的服务地址:443 \
  -e ECH_TOKEN=你的令牌 \
  -e ECH_ROUTING=bypass_cn \
  -e ECH_WEB=:9090 \
  -e ECH_PASSWORD=你的管理密码 \
  ghcr.io/dirige/ech-proxy-panel:latest
```

**优先级**：命令行参数 > 环境变量 > config.json

### 参数说明

| 参数 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| `-f` | `ECH_SERVER` | `hhech.nb1tap.kdns.fr:443` | 服务端地址 |
| `-token` | `ECH_TOKEN` | `honghongfree` | 身份验证令牌 |
| `-l` | `ECH_LISTEN` | `0.0.0.0:30000` | 代理监听地址 |
| `-ip` | `ECH_SERVER_IP` | （自动） | 优选 IP / 域名 |
| `-web` | `ECH_WEB` | （空） | Web 管理面板地址，如 `:9090` |
| `-routing` | `ECH_ROUTING` | `bypass_cn` | 分流模式 |
| `-dns` | `ECH_DNS` | `dns.alidns.com/dns-query` | DoH 服务器 |
| `-ech` | `ECH_DOMAIN` | `cloudflare-ech.com` | ECH 查询域名 |
| `-proxyip` | `ECH_PROXY_IP` | （空） | 固定出口 IP |
| `-password` | `ECH_PASSWORD` | （空） | 面板登录密码 |
| `-config` | `ECH_CONFIG` | `/data/config.json` | 配置文件路径 |
| `-rules-data` | `ECH_RULES_DATA` | `/data/rules.json` | 规则持久化路径 |

### 分流模式

| 模式 | 说明 |
|------|------|
| `bypass_cn` | **默认**。中国 IP 直连，其他走 ECH 代理 |
| `global` | 全局代理 |
| `none` | 全部直连 |
| `custom` | 自定义规则优先，无匹配时走代理 |

### 自定义规则

在 Web 面板「规则」页面添加，或编辑 `/data/rules.json`：

```
domain,baidu.com,direct
domain,google.com,proxy
domain,*.255432.xyz,proxy
```

- `domain`：精确匹配 + 子域名匹配（`255432.xyz` 会匹配 `emos.255432.xyz`）
- `*.domain`：通配符写法，效果同上
- 自定义规则在**所有分流模式**下优先生效

## 部署后修改配置

**方式一：Web 面板**

打开 `http://你的IP:9091`，在「配置」页面修改后保存。

**方式二：编辑配置文件**

```bash
docker exec -it ech-proxy vi /data/config.json
docker restart ech-proxy
```

## 服务端说明

本项目只包含客户端。服务端为 Cloudflare Worker（`_worker.js`），将 WebSocket 流量转发到 ECH 入口。

项目内 `_worker.js` 仅供参考，部署时请使用你自己的 Worker 实例。

## 致谢

- 原项目：[byJoey/ech-wk](https://github.com/byJoey/ech-wk)
- 免费 ECH 节点：[Telegram @honghongtg](https://t.me/honghongtg)
- 中国 IP 列表：[mayaxcn/china-ip-list](https://github.com/mayaxcn/china-ip-list)
- 原始代码来源：[CF_NAT](https://t.me/CF_NAT)
