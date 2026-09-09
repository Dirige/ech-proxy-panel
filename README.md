# ECH Proxy Panel

基于 [byJoey/ech-wk](https://github.com/byJoey/ech-wk) 二次修改，增加了 Web 管理面板和部分功能。

> 原项目是一个跨平台 ECH Workers 代理客户端。本项目在其基础上新增了 Web 面板、实时流量统计、自定义分流规则、出口 IP/COLO 检测等功能，更适合在软路由上通过 Docker 部署使用。

## 新增功能

- Web 管理面板（暗色主题）
- 实时上传/下载速度 + 总流量统计
- 活跃连接列表
- 简化的分流规则（直连域名 / 代理域名）
- 出口 IP 和 CF 落地节点（COLO）自动检测
- 固定出口 IP（ProxyIP）支持
- 配置持久化（config.json），重启不丢失
- 规则持久化（rules.json），重启不丢失
- 日志实时查看
- 内存使用监控

## Docker 部署

### 拉取镜像

```bash
docker pull ghcr.io/dirige/ech-proxy-panel:latest
```

### 首次部署

```bash
docker run -d \
  --name ech-proxy \
  --restart always \
  -p 9090:9090 \
  -p 30000:30000 \
  -v ech-data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest \
  -l 0.0.0.0:30000 \
  -f 你的服务地址:443 \
  -token 你的令牌 \
  -web :9090 \
  -routing global
```

首次运行后配置自动保存，后续重启直接：

```bash
docker restart ech-proxy
```

### 参数说明

| 参数 | 必填 | 说明 | 示例 |
|------|------|------|------|
| `-f` | 是 | 服务端地址 | `-f xxx.workers.dev:443` |
| `-token` | 否 | 身份验证令牌 | `-token your-token` |
| `-l` | 否 | 代理监听地址（默认 `127.0.0.1:30000`） | `-l 0.0.0.0:30000` |
| `-ip` | 否 | 优选 IP / 域名 | `-ip 172.64.229.240` |
| `-web` | 否 | 管理面板端口 | `-web :9090` |
| `-routing` | 否 | 分流模式 | `-routing global` |
| `-dns` | 否 | DoH 服务器 | `-dns dns.alidns.com/dns-query` |
| `-ech` | 否 | ECH 域名 | `-ech cloudflare-ech.com` |
| `-proxyip` | 否 | 固定出口 IP | `-proxyip 101.79.165.113:443` |

### 分流模式

| 模式 | 说明 |
|------|------|
| `global` | 全局代理（默认） |
| `bypass_cn` | 跳过中国大陆 |
| `none` | 不改变（直连） |
| `custom` | 自定义规则（需配合 `-rules` 参数） |

## 爱快 Docker 部署（桥接模式）

### 步骤 1：拉取镜像

```bash
docker pull ghcr.io/dirige/ech-proxy-panel:latest
```

### 步骤 2：首次部署（传参数）

```bash
docker run -d \
  --name ech-proxy \
  --restart always \
  -p 9090:9090 \
  -p 30000:30000 \
  -v ech-data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest \
  -l 0.0.0.0:30000 \
  -f 你的服务地址:443 \
  -token 你的令牌 \
  -web :9090 \
  -routing global
```

### 步骤 3：后续重启

配置已保存，无需再传参数：

```bash
docker restart ech-proxy
```

### 步骤 4：修改配置

**方式一：Web 面板**

浏览器打开 `http://爱快IP:9090`，在「配置」页面修改，点保存。

**方式二：手动编辑配置文件**

```bash
docker exec -it ech-proxy vi /data/config.json
docker restart ech-proxy
```

### 配置文件模板

`/data/config.json`：

```json
{
  "listen_addr": "0.0.0.0:30000",
  "server_addr": "你的服务地址:443",
  "server_ip": "172.64.229.240",
  "token": "你的令牌",
  "dns_server": "dns.alidns.com/dns-query",
  "ech_domain": "cloudflare-ech.com",
  "routing_mode": "global",
  "web_addr": ":9090",
  "proxy_ip": ""
}
```

字段说明：

| 字段 | 说明 | 示例 |
|------|------|------|
| `listen_addr` | 代理监听地址 | `0.0.0.0:30000` |
| `server_addr` | 服务端地址 | `xxx.workers.dev:443` |
| `server_ip` | 优选 IP（留空自动分配） | `172.64.229.240` |
| `token` | 认证令牌 | `your-token` |
| `dns_server` | DoH 服务器 | `dns.alidns.com/dns-query` |
| `ech_domain` | ECH 域名 | `cloudflare-ech.com` |
| `routing_mode` | 分流模式 | `global` / `bypass_cn` / `none` / `custom` |
| `web_addr` | 管理面板端口 | `:9090` |
| `proxy_ip` | 固定出口 IP（留空自动分配） | `101.79.165.113:443` |

### 固定出口 IP

在 config.json 中设置 `proxy_ip` 字段，或在 Web 面板「配置」页面修改。

### 自定义规则文件

创建规则文件，每行一条：

```
domain,baidu.com,direct
domain,google.com,proxy
domain,github.com,proxy
```

格式：`domain,域名,动作`（动作：`direct` 直连 / `proxy` 代理）

挂载到容器：

```bash
docker run -d \
  --name ech-proxy \
  --restart always \
  -p 9090:9090 \
  -p 30000:30000 \
  -v ech-data:/data \
  -v /path/to/rules.txt:/etc/rules.txt \
  ghcr.io/dirige/ech-proxy-panel:latest \
  -l 0.0.0.0:30000 \
  -f 你的服务地址:443 \
  -token 你的令牌 \
  -web :9090 \
  -routing custom \
  -rules /etc/rules.txt
```

## 管理面板

部署后访问 `http://你的IP:9090`，面板包含：

- **概览**：上传/下载速度、总量、活动连接、内存、出口 IP、COLO、网络延迟
- **连接**：当前活跃连接列表
- **规则**：直连域名 / 代理域名管理
- **配置**：分流模式、固定出口 IP、DoH、ECH 域名等

## 致谢

- 原项目：[byJoey/ech-wk](https://github.com/byJoey/ech-wk)
- 原始代码来源：[CF_NAT](https://t.me/CF_NAT)
- 中国 IP 列表：[mayaxcn/china-ip-list](https://github.com/mayaxcn/china-ip-list)
