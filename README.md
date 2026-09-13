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
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 9091:9090 \
  -v ech-data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest
```

首次运行使用内置默认配置（`hhech.nb1tap.kdns.fr:443` / `honghongfree`），打开 `http://你的IP:9091` 管理。

### Docker Compose

```bash
docker compose up -d
```

### 自定义参数

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

### 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-f` | `hhech.nb1tap.kdns.fr:443` | 服务端地址（服务端由 Cloudflare Worker 提供） |
| `-token` | `honghongfree` | 身份验证令牌 |
| `-l` | `0.0.0.0:30000` | 代理监听地址 |
| `-ip` | （自动） | 优选 IP / 域名 |
| `-web` | （空） | Web 管理面板地址，如 `:9090` |
| `-routing` | `bypass_cn` | 分流模式 |
| `-dns` | `dns.alidns.com/dns-query` | DoH 服务器 |
| `-ech` | `cloudflare-ech.com` | ECH 查询域名 |
| `-proxyip` | （空） | 固定出口 IP，如 `101.79.165.113:443` |
| `-password` | （空） | 面板登录密码，留空无需登录 |

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
