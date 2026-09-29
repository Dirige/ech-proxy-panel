# ECH Proxy Panel

基于 [byJoey/ech-wk](https://github.com/byJoey/ech-wk) 二次修改，增加了 Web 管理面板和部分功能。

> 免费 ECH 节点由 [Telegram 频道](https://t.me/honghongtg) 提供，感谢维护！
>
> 本项目仅在原项目基础上补充了 Web 面板、流量统计、分流规则、出口检测等客户端功能，Cloudflare Worker 服务端保持不动。

## 功能

- **SOCKS5 + HTTP 代理**：统一监听，自动识别
- **分流模式**：`bypass_cn`（默认，跳过中国大陆）/ `global` / `none` / `custom`
- **自定义规则**：`domain` / `ipcidr` / `keyword` / `cfip` 四种类型，动作 `direct`（直连）/ `proxy`（ECH）/ `upstream`（本地上游），支持 `*.example.com` 通配符与子域名匹配
- **分流命中可观测**：连接列表显示每条连接的出站（直连/代理/兜底/上游）与命中依据（哪条规则或哪个模式）
- **连接老化自愈**：单连接到量（默认 800M）或到时主动断开，播放器自动重连拿新 invocation，缓解越跑越慢
- **Clash 兜底**：ECH 隧道拨号失败 / Worker 返回 ERROR 时，自动改走本地 Clash（SOCKS5 或 HTTP 上游）继续传输
- **Web 管理面板**：暗色主题，概览/连接/规则/配置四个页面
- **实时监控**：上传下载速度、总流量、活跃连接、内存、出口 IP/COLO、延迟
- **登录鉴权**：可选密码保护（不设密码则无需登录）
- **配置持久化**：`/data/config.json` + `/data/rules.json`，重启不丢失
- **中国 IP 列表**：内置 CIDR 格式快照，启动后经 ECH 隧道后台自动更新

> 限制：UDP 代理目前仅支持 DNS（53 端口），其他 UDP 流量不转发。

## 快速开始

### Docker（推荐）

```bash
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 30001:30001 \
  -v ech-data:/data \
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
  -p 30000:30000 -p 30001:30001 \
  -v ech-data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest \
  -f 你的服务地址:443 \
  -token 你的令牌 \
  -web :30001 \
  -routing bypass_cn \
  -password 你的管理密码
```

**方式二：环境变量**

```bash
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 30001:30001 \
  -v ech-data:/data \
  -e ECH_SERVER=你的服务地址:443 \
  -e ECH_TOKEN=你的令牌 \
  -e ECH_ROUTING=bypass_cn \
  -e ECH_WEB=:30001 \
  -e ECH_PASSWORD=你的管理密码 \
  ghcr.io/dirige/ech-proxy-panel:latest
```

| 环境变量      | 命令行参数   | 默认值                    | 说明                 |
| ------------- | ------------ | ------------------------- | -------------------- |
| `ECH_SERVER`  | `-f`         | `hhech.nb1tap.kdns.fr:443` | 服务端地址          |
| `ECH_TOKEN`   | `-token`     | `honghongfree`            | 身份验证令牌         |
| `ECH_LISTEN`  | `-l`         | `0.0.0.0:30000`           | 代理监听地址         |
| `ECH_SERVER_IP` | `-ip`      | （自动）                  | 优选 IP / 域名       |
| `ECH_WEB`     | `-web`       | `:30001`                   | Web 面板监听地址     |
| `ECH_ROUTING` | `-routing`   | `bypass_cn`               | 分流模式             |
| `ECH_DNS`     | `-dns`       | `dns.alidns.com/dns-query` | DoH 服务器          |
| `ECH_DOMAIN`  | `-ech`       | `cloudflare-ech.com`      | ECH 查询域名         |
| `ECH_PROXY_IP` | `-proxyip`  | （空）                    | 固定出口 IP          |
| `ECH_PASSWORD` | `-password` | （空）                    | 面板登录密码         |
| `ECH_RECYCLE` | `-recycle`    | `on`                  | 老化自愈总开关（`on`/`off`） |
| `ECH_RECYCLE_BYTES` | `-recycle-bytes` | `800m`         | 单连接流量阈值，达到即断开等重连（`0`=关闭，支持 `512m`/`2g`） |
| `ECH_RECYCLE_DURATION` | `-recycle-duration` | `0`      | 单连接时长阈值，如 `30m`（`0`=关闭） |
| `ECH_UPSTREAM` | `-upstream`   | （空）                  | ECH 失败时的兜底上游：`127.0.0.1:7890`（SOCKS5）、`socks5://127.0.0.1:7890`、`http://127.0.0.1:7891` |
| `ECH_FALLBACK` | `-fallback`   | `on`                   | 兜底开关（`on`/`off`；需先配置 `-upstream`） |
| `ECH_RULES_DATA` | `-rules-data` | `/data/rules.json`    | 面板规则持久化文件（面板可编辑的规则桶） |
| —              | `-rules`      | （空）                  | CSV 规则文件（只读桶，`routing=custom` 时加载；与面板规则合并生效，互不覆盖） |

**优先级**：命令行参数 > 环境变量 > config.json

### 参数说明

| 参数 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| `-f` | `ECH_SERVER` | `hhech.nb1tap.kdns.fr:443` | 服务端地址 |
| `-token` | `ECH_TOKEN` | `honghongfree` | 身份验证令牌 |
| `-l` | `ECH_LISTEN` | `0.0.0.0:30000` | 代理监听地址 |
| `-ip` | `ECH_SERVER_IP` | （自动） | 优选 IP / 域名 |
| `-web` | `ECH_WEB` | （空） | Web 管理面板地址，如 `:30001` |
| `-routing` | `ECH_ROUTING` | `bypass_cn` | 分流模式 |
| `-dns` | `ECH_DNS` | `dns.alidns.com/dns-query` | DoH 服务器 |
| `-ech` | `ECH_DOMAIN` | `cloudflare-ech.com` | ECH 查询域名 |
| `-proxyip` | `ECH_PROXY_IP` | （空） | 固定出口 IP |
| `-password` | `ECH_PASSWORD` | （空） | 面板登录密码 |
| `-config` | `ECH_CONFIG` | `/data/config.json` | 配置文件路径 |
| `-rules-data` | `ECH_RULES_DATA` | `/data/rules.json` | 规则持久化路径 |

---

## 使用说明

### 代理设置

- 类型：SOCKS5 或 HTTP，地址 `127.0.0.1`，端口 `30000`
- 面板地址：`http://你的IP:30001`

### 面板页面

- **概览**：实时速度、总流量、活跃连接、内存、出口 IP / 落地节点、延迟，以及实时日志（出口信息只在查看概览时按需检测并缓存 10 分钟，平时不产生探测连接；可点「重新检测出口」手动刷新）
- **连接**：活跃连接列表，支持搜索、排序、暂停刷新；「规则」列显示直连/代理/上游/兜底，「命中」列显示具体命中依据（如 `规则 domain:example.com → 直连`、`模式 bypass_cn(中国IP) → 直连`）
- **规则**：编辑面板规则——直连/代理/上游域名三列 + IP 段/CF IP 段/关键词规则表 + CF IP 段管理（内置快照可增删），支持未保存提示，保存即生效
- **配置**：修改服务端、令牌、分流模式等，保存后自动刷新 ECH（监听地址类改动需重启容器）


### 分流模式

| 模式 | 说明 |
|------|------|
| `bypass_cn` | **默认**。中国 IP 直连，其他走 ECH 代理 |
| `global` | 全局代理 |
| `none` | 全部直连 |
| `custom` | 自定义规则优先，无匹配时走代理 |

### 分流规则

规则有**两个来源**，分开存放、合并生效，互不覆盖：

| 来源 | 位置 | 是否可编辑 |
| ---- | ---- | ---------- |
| 文件规则 | `-rules` 指定的 CSV（`type,value,action`，每行一条） | 只读：改文件后用面板「重载规则」或重启 |
| 面板规则 | `-rules-data`（默认 `/data/rules.json`），即「规则」页保存的内容 | 面板可增删改，保存即生效 |

匹配顺序：**文件规则优先，面板规则其次**；命中即停。规则本身在**所有分流模式**下都优先生效（先查规则，再看模式）。

规则类型：

| 类型 | 示例 | 说明 |
| ---- | ---- | ---- |
| `domain` | `example.com` / `*.example.com` | 同时匹配子域名（`example.com` 匹配 `www.example.com`，两种写法等价） |
| `ipcidr` | `10.0.0.0/8`、`127.0.0.0/8` | 命中 IP 字面量，也命中解析到该网段的**域名目标**（按规则顺序解析回查） |
| `keyword` | `tracker` | 主机名包含该子串即命中 |
| `cfip` | `cfip,,upstream`（值仅备注，可留空） | 目标落在**生效 CF 网段**（内置 Cloudflare 网段快照，来源 https://www.cloudflare.com/ips/ ，页面标注最后更新 2023-09-28，含 v4 15 段 + v6 7 段；规则页「CF IP 段管理」可增删，生效集 = 内置 ∪ 新增 − 删除）即命中，域名目标先解析再比对 |

动作（即出站方式）：

| 动作 | 含义 | 连接页「规则」列 |
| ---- | ---- | ---- |
| `direct` | 本地直连 | 直连 |
| `proxy` | 走 ECH 隧道 | 代理 |
| `upstream` | 走 `-upstream` 指定的本地代理（Clash 等）；**未配置或拨不通自动回退 ECH**，不受 `-fallback` 开关影响 | 上游 |

### 完整决策链（无遗漏）

先按序匹配规则，未命中再看模式；每条连接只会走其中一条路：

| 顺序 | 条件 | 出站 | 失败后的处理 |
| ---- | ---- | ---- | ---- |
| 1 | 文件规则命中 `action=direct` | 直连 | 失败回 502（无兜底） |
| 2 | 文件规则命中 `action=proxy` | ECH | ECH 失败 → 配置了 `-upstream` 且 `-fallback=on` 时改走上游 → 仍失败回 502 |
| 3 | 文件规则命中 `action=upstream` | 本地上游 | 拨不通 → 自动回退 ECH → ECH 再失败则再试上游 → 都失败回 502 |
| 4 | 面板规则命中（任一动作） | 同 1~3 | 同 1~3 |
| 5 | 未命中，模式 `none` | 直连 | 失败回 502 |
| 6 | 未命中，模式 `global` / `custom` | ECH | 同 2 |
| 7 | 未命中，模式 `bypass_cn` 且目标是中国 IP | 直连 | 失败回 502 |
| 8 | 未命中，模式 `bypass_cn` 且目标是海外 IP / 解析失败 | ECH | 同 2 |

典型组合——**Cloudflare IP 段走 Clash、其余走 ECH**：加一条 `cfip` + `upstream` 规则（动作选「上游代理」），其余流量自然落到模式默认的 ECH；**ECH 挂了也要通**再叠加 `-upstream` + `-fallback on` 兜底即可，两层互不干扰。

> 说明：`-fallback off` 只关「ECH 失败兜底」，不影响 `action=upstream` 的规则出站；反之规则出站也不需要开 `-fallback`。

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

打开 `http://你的IP:30001`，在「配置」页面修改后保存。

**方式二：编辑配置文件**

```bash
docker exec -it ech-proxy vi /data/config.json
docker restart ech-proxy
```

## 服务端说明

本项目只包含客户端。服务端为 Cloudflare Worker（`_worker.js`），将 WebSocket 流量转发到 ECH 入口。

项目内 `_worker.js` 仅供参考，部署时请使用你自己的 Worker 实例。

### 视频卡顿 / 单条连接跑到 1GB 后降速

这套架构是 **一条本地连接 = 一条 WebSocket = Worker 的一次 invocation**，全程不换。
Cloudflare 的资源配额是**按 invocation 计**的，所以同一条连接搬运的数据越多，那条 invocation
累积的 CPU 消耗和发送队列就越多，表现就是：**不断流，但吞吐逐步下降、开始缓冲**。

判断方法：卡顿时**别关掉，直接再开一路**。新的一路立刻流畅 → 是这条 invocation 老化；
新的一路也一样慢 → 是这个节点 / CF 出口 IP 那个时段拥塞，换节点或错峰。

可用的缓解手段：

| 手段 | 做法 |
| ---- | ---- |
| 连接老化自愈（默认开） | `-recycle-bytes 800m`：单连接累计到量即主动断开，播放器自动重连拿全新 invocation（日志可见 `[老化]` 行） |
| 应用层分片 | Emby 等播放器改用 HLS/转码分片，每个分片是独立请求 = 独立 invocation，不会撞累积上限 |
| 换出口 / 换节点 | `-proxyip` 或改 `ECH_SERVER`，换一份全新的 invocation 资源 |

> 注：如果服务端 Worker 是你自己部署的，还可以直接改服务端：
> `_worker.js` 里发送前检查 `webSocket.getBufferedAmount()` 做背压，
> 并把 Worker 的 `limits.cpu_ms` 调到 300000（默认只有 30s）。
> 用免费节点时这两项改不了，只能在客户端侧按上表处理。


## 连接老化自愈

单条本地连接 = 一条 WebSocket = Worker 的一次 invocation，数据搬得越多 invocation 越老化。
开启后（默认 `on`）单连接达到阈值会**主动断开**，播放器立刻重连拿新 invocation：

| 参数 | 环境变量 | 默认 | 说明 |
| ---- | -------- | ---- | ---- |
| `-recycle-bytes` | `ECH_RECYCLE_BYTES` | `800m` | 流量阈值，支持 `512m`/`2g`/`100k`，`0`=关闭 |
| `-recycle-duration` | `ECH_RECYCLE_DURATION` | `0` | 时长阈值，如 `30m`，`0`=关闭 |
| `-recycle` | `ECH_RECYCLE` | `on` | 总开关，`off` 完全恢复旧行为 |

- 两个阈值任一达到即断开（各自为 `0` 时该项不计），日志形如 `[老化] ...已传输 X（阈值 Y），主动断开等待播放器重连`
- **两个阈值也可在面板「配置」页直接修改（保存即生效，无需重启）**；优先级：命令行 > 环境变量 > 配置文件（仅当前者为默认值时配置文件才生效）
- 非法输入只打 `[警告]` 并回落默认值，不影响启动
- 代价：到量瞬间约 1s 的播放器重连闪断
- 经验值：CF 免费节点单隧道约 150~200MB 后吞吐明显衰减，可从 `150m` + `15m` 起步调优

---

## ECH 失败兜底（Clash）

ECH 隧道拨号失败、或 Worker 返回 `ERROR:`（CF 拒连目标）时，可自动改走本地 Clash 继续传输，客户端无感：

| 参数 | 环境变量 | 默认 | 说明 |
| ---- | -------- | ---- | ---- |
| `-upstream` | `ECH_UPSTREAM` | （空） | 上游地址：`127.0.0.1:7890`（按 SOCKS5 解析）、`socks5://127.0.0.1:7890`、`http://127.0.0.1:7891`（HTTP CONNECT）；可带 `user:pass@` |
| `-fallback` | `ECH_FALLBACK` | `on` | 总开关；`off` 时失败直接回 502（旧行为）。必须配置了 `-upstream` 才会兜底 |

- 只兜 **ECH 侧**：直连失败仍回 502（反向兜底暂缓）
- 兜底成功的连接在「连接」页规则列显示 `兜底`，日志有 `[兜底]` 拨号记录；规则命中 `upstream` 出站的显示 `上游`，日志有 `[上游]` 记录
- 非法 `-upstream` 只打 `[警告]`，不影响启动
- 与 `action=upstream` 规则的关系：`-fallback off` 只关本节兜底，规则出站照常生效；规则出站拨不通时自动回退 ECH（日志 `[上游] ...回退 ECH`）
- 也可在面板「配置」页直接填写上游地址和兜底开关，保存即生效并持久化到 `/data/config.json`

---

## 致谢

- 原项目：[byJoey/ech-wk](https://github.com/byJoey/ech-wk)
- 免费 ECH 节点：[Telegram @honghongtg](https://t.me/honghongtg)
- 中国 IP 列表：[mayaxcn/china-ip-list](https://github.com/mayaxcn/china-ip-list)
- 原始代码来源：[CF_NAT](https://t.me/CF_NAT)
