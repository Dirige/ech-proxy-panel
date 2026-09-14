# ECH Proxy Panel

基于 [byJoey/ech-wk](https://github.com/byJoey/ech-wk) 二次开发，增加了 Web 管理面板和流量统计、分流规则、出口检测等客户端功能。

> 免费 ECH 节点由 [Telegram 频道](https://t.me/honghongtg) 提供，感谢维护！
>
> 服务端为 Cloudflare Worker（仓库内 `_worker.js` 仅供参考），请使用你自己的 Worker 实例。

---

## 功能特性

| 功能 | 说明 |
| ---- | ---- |
| SOCKS5 + HTTP 代理 | 统一监听一个端口，自动识别协议 |
| 分流模式 | `bypass_cn`（默认）/ `global` / `none` / `custom` |
| 自定义规则 | 面板增删直连 / 代理域名，支持 `*.example.com` 通配符与子域名匹配 |
| Web 管理面板 | 暗色主题，概览 / 连接 / 规则 / 配置四个页面 |
| 实时监控 | 上传下载速度、总流量、活跃连接、内存、出口 IP / COLO、延迟 |
| 登录保护 | 可选管理密码，不设密码则无需登录 |
| 配置持久化 | `/data/config.json` + `/data/rules.json`，重启不丢失 |
| 中国 IP 列表 | 内置快照，后台自动更新 |

---

## 部署教程（Docker）

镜像默认同时启动代理（`:30000`）和 Web 面板（`:9090`），首次运行使用内置默认配置，开箱即用。

### 方式一：Docker Run

```bash
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 9091:9090 \
  -v ech-data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest
```

启动后打开 `http://你的IP:9091` 进入管理面板。

### 方式二：Docker Compose

`docker-compose.yml`：

```yaml
services:
  ech-proxy:
    image: ghcr.io/dirige/ech-proxy-panel:latest
    container_name: ech
    restart: always
    ports:
      - "9091:9090"
      - "30000:30000"
    volumes:
      - ./data:/data
```

```bash
docker compose up -d
```

### 自定义配置

推荐用环境变量覆盖默认配置：

```bash
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 9091:9090 \
  -v ech-data:/data \
  -e ECH_SERVER=你的服务地址:443 \
  -e ECH_TOKEN=你的令牌 \
  -e ECH_ROUTING=bypass_cn \
  -e ECH_PASSWORD=你的管理密码 \
  ghcr.io/dirige/ech-proxy-panel:latest
```

| 环境变量      | 命令行参数   | 默认值                    | 说明                 |
| ------------- | ------------ | ------------------------- | -------------------- |
| `ECH_SERVER`  | `-f`         | `hhech.nb1tap.kdns.fr:443` | 服务端地址          |
| `ECH_TOKEN`   | `-token`     | `honghongfree`            | 身份验证令牌         |
| `ECH_LISTEN`  | `-l`         | `0.0.0.0:30000`           | 代理监听地址         |
| `ECH_SERVER_IP` | `-ip`      | （自动）                  | 优选 IP / 域名       |
| `ECH_WEB`     | `-web`       | `:9090`                   | Web 面板监听地址     |
| `ECH_ROUTING` | `-routing`   | `bypass_cn`               | 分流模式             |
| `ECH_DNS`     | `-dns`       | `dns.alidns.com/dns-query` | DoH 服务器          |
| `ECH_DOMAIN`  | `-ech`       | `cloudflare-ech.com`      | ECH 查询域名         |
| `ECH_PROXY_IP` | `-proxyip`  | （空）                    | 固定出口 IP          |
| `ECH_PASSWORD` | `-password` | （空）                    | 面板登录密码         |
| `ECH_DOWN_LIMIT` | `-downlimit` | `0`                     | 单连接下行限速 Mbps（`0`=不限） |
| `ECH_IDLE_TIMEOUT` | `-idle-timeout` | `15m`              | 隧道空闲超时（`0`=永不主动关闭） |

优先级：**命令行参数 > 环境变量 > config.json**。

---

## 使用说明

### 代理设置

- 类型：SOCKS5 或 HTTP，地址 `127.0.0.1`，端口 `30000`
- 面板地址：`http://你的IP:9091`

### 面板页面

- **概览**：实时速度、总流量、活跃连接、内存、出口 IP / 落地节点、延迟，以及实时日志
- **连接**：活跃连接列表，支持搜索、排序、暂停刷新
- **规则**：增删直连 / 代理域名，保存即生效
- **配置**：修改服务端、令牌、分流模式等，保存后自动刷新 ECH（监听地址类改动需重启容器）

### 分流模式

| 模式         | 说明                                   |
| ------------ | -------------------------------------- |
| `bypass_cn`  | 默认。中国 IP 直连，其他走 ECH 代理    |
| `global`     | 全局代理                               |
| `none`       | 全部直连                               |
| `custom`     | 自定义规则优先，无匹配时走代理         |

自定义规则在**所有分流模式**下优先生效，`domain` 类型同时匹配子域名
（`example.com` 会匹配 `www.example.com`，`*.example.com` 写法效果相同）。

### 修改配置

- **面板修改**：打开 `http://你的IP:9091`，在「配置」页修改后保存
- **文件修改**：

```bash
docker exec -it ech-proxy vi /data/config.json
docker restart ech-proxy
```

---

### 视频卡顿 / 单条连接跑到 1GB 后降速

这套架构是 **一条本地连接 = 一条 WebSocket = Worker 的一次 invocation**，全程不换。
Cloudflare 的资源配额是**按 invocation 计**的，所以同一条连接搬运的数据越多，那条 invocation
累积的 CPU 消耗和发送队列就越多，表现就是：**不断流，但吞吐逐步下降、开始缓冲**。

判断方法：卡顿时**别关掉，直接再开一路**。新的一路立刻流畅 → 是这条 invocation 老化；
新的一路也一样慢 → 是这个节点 / CF 出口 IP 那个时段拥塞，换节点或错峰。

可用的缓解手段：

| 手段 | 做法 |
| ---- | ---- |
| 下行限速（推荐先试） | `-downlimit 15`，设为视频码率的 **1.2~1.5 倍**。限速后播放器始终能立即消费，worker 侧的发送队列不会堆积，这条 invocation 就不容易老化 |
| 应用层分片 | Emby 等播放器改用 HLS/转码分片，每个分片是独立请求 = 独立 invocation，不会撞累积上限 |
| 换出口 / 换节点 | `-proxyip` 或改 `ECH_SERVER`，换一份全新的 invocation 资源 |
| 空闲超时兜底 | `-idle-timeout` 默认 15 分钟，卡死的长连接会被自动回收 |

> 注：如果服务端 Worker 是你自己部署的，还可以直接改服务端：
> `_worker.js` 里发送前检查 `webSocket.getBufferedAmount()` 做背压，
> 并把 Worker 的 `limits.cpu_ms` 调到 300000（默认只有 30s）。
> 用免费节点时这两项改不了，只能在客户端侧按上表处理。

---

## 致谢

- 原项目：[byJoey/ech-wk](https://github.com/byJoey/ech-wk)
- 免费 ECH 节点：[Telegram @honghongtg](https://t.me/honghongtg)
- 中国 IP 列表：[mayaxcn/china-ip-list](https://github.com/mayaxcn/china-ip-list)
- 原始代码来源：[CF_NAT](https://t.me/CF_NAT)
