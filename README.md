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
- 日志实时查看
- 内存使用监控

## Docker 部署

### 拉取镜像

```bash
docker pull ghcr.io/dirige/ech-proxy-panel:latest
```

### 运行

```bash
docker run -d \
  --name ech-proxy \
  --restart always \
  -p 9090:9090 \
  -p 30000:30000 \
  ghcr.io/dirige/ech-proxy-panel:latest \
  -l 0.0.0.0:30000 \
  -f 你的服务地址:443 \
  -token 你的令牌 \
  -web :9090 \
  -routing global
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
| `-rules` | 否 | 自定义规则文件（routing=custom 时需要） | `-rules /etc/rules.txt` |

### 分流模式

| 模式 | 说明 |
|------|------|
| `global` | 全局代理（默认） |
| `bypass_cn` | 跳过中国大陆 |
| `none` | 不改变（直连） |
| `custom` | 自定义规则（需配合 `-rules` 参数） |

### 固定出口 IP

如果需要固定出口 IP（例如固定到某个香港节点），使用 `-proxyip` 参数：

```bash
docker run -d \
  --name ech-proxy \
  --restart always \
  -p 9090:9090 \
  -p 30000:30000 \
  ghcr.io/dirige/ech-proxy-panel:latest \
  -l 0.0.0.0:30000 \
  -f 你的服务地址:443 \
  -token 你的令牌 \
  -web :9090 \
  -proxyip 101.79.165.113:443
```

留空则使用自动就近分配。

### 自定义规则文件

创建规则文件，每行一条：

```
domain,baidu.com,direct
domain,google.com,proxy
domain,github.com,proxy
```

格式：`domain,域名,动作`（动作：`direct` 直连 / `proxy` 代理）

然后挂载到容器：

```bash
docker run -d \
  --name ech-proxy \
  --restart always \
  -p 9090:9090 \
  -p 30000:30000 \
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
