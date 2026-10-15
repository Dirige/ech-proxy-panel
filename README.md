# ECH Proxy Panel

基于 [byJoey/ech-wk](https://github.com/byJoey/ech-wk) 原版源码的二次修改：**原版 `ech-workers.go` 逐字节保留（仅 4 处标记为 `[面板钩子 A~D]` 的接入点，共约 10 行）**，在其上叠加「分流规则补充（直连列表 / 代理列表）」、「不影响代理行为的 Web 管理面板」与「每日定时自重启」三块功能。面板代码全部集中在单个 `panel.go` 中，与原版做 diff 对照更直观。

> 免费 ECH 节点由 [Telegram 频道](https://t.me/honghongtg) 提供，感谢维护！
>
> Cloudflare Worker 服务端（`_worker.js`）保持原版不动。

## 功能

- **原版代理数据面原样保留**：SOCKS5 + HTTP 统一监听、DoH 查询 ECH 配置、WebSocket 隧道转发，代理行为与原版一致；仅在连接入口经钩子插入规则判定与统计埋点
- **分流规则补充**：`domain` / `ipcidr` / `keyword` 三种类型，动作仅 `direct`（直连）/ `proxy`（ECH）两种出站；规则在**所有分流模式**下优先于模式生效
- **分流模式（原版 3 种 + 面板新增 1 种）**：`global`（原版默认）/ `bypass_cn` / `none` + 面板新增 `custom`
- **Web 管理面板**：暗色主题，概览（含实时日志） / 连接 / 流量 / 规则配置 共四个页面
- **连接观测**：活跃连接列表显示实拨地址、出站方式与**命中依据**（哪条规则 / 哪个模式），支持搜索、排序、暂停刷新
- **实时日志**：并入概览页，最近 200 条运行日志，级别筛选，3 秒自动刷新
- **流量页**：实时上传/下载速度、总量、活动连接、运行时长卡片 + 上传/下载速度趋势图（ws 每 2 秒采样）
- **出口 IP 检测卡片**：出口 IP + Cloudflare COLO，按需检测并缓存 10 分钟
- **登录鉴权**：可选密码保护（不设密码则无需登录）
- **配置持久化**：`/data/config.json` + `/data/rules.json`，面板修改保存即生效，重启不丢失
- **每日定时自重启**：默认 `05:00` 进程内 re-exec 自重启，**不依赖容器 restart 策略**
- **参数优先级（原版 flag 语义为王）**：命令行 > 环境变量 `ECH_*` > 配置文件 > 原版 flag 默认值；无任何隐藏硬编码默认，`-f` 未提供且配置里也没有时按原版行为直接报错退出
- **中国 IP 列表**：原版 loader 识别的「起始IP 结束IP」两列格式（与上游 mayaxcn 源格式一致），按 `程序目录 → 当前目录` 查找，缺失时自动下载

> 限制：UDP 代理目前仅支持 DNS（53 端口），其他 UDP 流量不转发。

## 交付物

| 交付物 | 说明 |
| ------ | ---- |
| GitHub Release | 推 `v*` tag 自动发布到 Releases，含 `amd64` + `arm64` 的 `tar.gz`（含 `chn_ip.txt` / `chn_ip_v6.txt` / `_worker.js` / `README.md` / `config.example.json`） |
| Docker 镜像 | push 到 main 自动构建并推送 GHCR（`latest` / `1.6` / sha tag） |
| Linux 独立二进制 | 同一工作流同时上传 Actions Artifacts（保留 90 天，需登录查看），与 Release 产物一致 |

## 快速开始

### Docker（推荐）

```bash
docker run -d --name ech-proxy --restart always \
  -p 30000:30000 -p 30001:30001 \
  -v ech-data:/data \
  ghcr.io/dirige/ech-proxy-panel:latest
```

首次运行时若挂载的 `/data` 里没有 `config.json`，会自动使用镜像内置配置（`/etc/ech/config.json`，即 `config.example.json`：`hhech.nb1tap.kdns.fr:443` / `honghongfree` / `0.0.0.0:30000` / `bypass_cn`），打开 `http://你的IP:30001` 管理。之后以你自己的 `/data/config.json` 为准。

### Docker Compose

```bash
docker compose up -d
```

### Linux 二进制

从 Actions Artifacts 下载对应架构的 `tar.gz`，解压后：

```bash
chmod +x ech-workers
./ech-workers -web :30001 -config ./config.json -rules-data ./rules.json
```

> 二进制按原版顺序查找 `chn_ip.txt`（程序目录 → 当前目录），缺失时自动从上游下载，建议保持解压目录完整。

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

### 参数总表

| 参数 | 环境变量 | 默认值 | 说明 |
| ---- | -------- | ------ | ---- |
| `-l` | `ECH_LISTEN` | `127.0.0.1:30000`（原版默认） | 代理监听地址（SOCKS5 + HTTP）；镜像内置配置为 `0.0.0.0:30000` |
| `-f` | `ECH_SERVER` | （空，必填） | 服务端地址；命令行/环境变量/配置文件都没有时按原版行为报错退出，镜像内置配置提供默认 |
| `-ip` | `ECH_SERVER_IP` | （空） | 优选 IP / 域名（绕过 DNS 解析） |
| `-token` | `ECH_TOKEN` | （空） | 身份验证令牌；镜像内置配置提供默认 |
| `-dns` | `ECH_DNS` | `dns.alidns.com/dns-query` | ECH 查询 DoH 服务器 |
| `-ech` | `ECH_DOMAIN` | `cloudflare-ech.com` | ECH 查询域名 |
| `-routing` | `ECH_ROUTING` | `global`（原版默认） | 分流模式：`global` / `bypass_cn` / `none` / `custom`；镜像内置配置为 `bypass_cn` |
| `-web` | `ECH_WEB` | `:30001`（未指定时） | Web 面板监听地址，如 `:30001` 或 `30001` |
| `-password` | `ECH_PASSWORD` | （空） | 面板登录密码（为空则不需登录） |
| `-config` | `ECH_CONFIG` | `/data/config.json` | 配置文件路径 |
| `-rules` | — | （空） | CSV 规则文件（只读桶，`type,value,action` 每行一条） |
| `-rules-data` | `ECH_RULES_DATA` | `/data/rules.json` | 面板规则持久化文件（面板可编辑的规则桶） |
| `-restart-at` | `ECH_RESTART_AT` | `05:00` | 每日定时重启（容器本地时区 `HH:MM`，`off`=关闭；进程内 re-exec 自重启） |

**优先级**：命令行参数（显式传入才生效）> 环境变量 > config.json > flag 默认值；配置文件只填充「命令行与环境变量都没提供」的项（旧配置文件里的 `proxy_ip` / `upstream` / `fallback` / `recycle_*` 字段会被自动忽略，保存时剔除）

## 使用说明

### 代理设置

- 类型：SOCKS5 或 HTTP，地址 `127.0.0.1`，端口 `30000`
- 面板地址：`http://你的IP:30001`

### 面板页面

- **概览**：实时速度、总流量、活跃连接、内存、出口 IP / 落地节点、延迟（出口信息只在查看概览时按需检测并缓存 10 分钟，可点「重新检测出口」手动刷新）+ 底部实时日志（最近 200 条，级别筛选，3 秒自动刷新）
- **连接**：活跃连接列表；「规则」列显示直连/代理，「命中」列显示具体依据（如 `规则 domain:example.com → 直连`、`模式 bypass_cn → 直连`）
- **流量**：实时速度与总量卡片，上传/下载速度趋势图（每 2 秒采样，约保留 5 分钟）
- **规则配置**：上半部分为系统配置——修改服务端、令牌、分流模式等，保存后自动刷新 ECH（监听地址类改动需重启容器）；下半部分为分流规则——直连/代理域名两列 + IP 段 / 关键词规则表（域名支持通配符 `*.example.com`），支持未保存提示、保存即生效

### 分流模式

| 模式 | 说明 |
| ---- | ---- |
| `global` | **原版默认**。全局代理（Docker 镜像内置配置改为 `bypass_cn`） |
| `bypass_cn` | 中国 IP 直连，其他走 ECH 代理 |
| `none` | 全部直连 |
| `custom` | 面板新增：自定义规则优先，未命中时默认走代理（等价于 global + 规则） |

### 分流规则

规则有**两个来源**，分开存放、合并生效，互不覆盖：

| 来源 | 位置 | 是否可编辑 |
| ---- | ---- | ---------- |
| 文件规则 | `-rules` 指定的 CSV（`type,value,action`，每行一条） | 只读：改文件后重启生效 |
| 面板规则 | `-rules-data`（默认 `/data/rules.json`），即「规则配置」页保存的内容 | 面板可增删改，保存即生效 |

匹配顺序：**文件规则优先，面板规则其次**；命中即停。规则本身在**所有分流模式**下都优先生效（先查规则，再走原版模式逻辑）。

规则类型：

| 类型 | 示例 | 说明 |
| ---- | ---- | ---- |
| `domain` | `example.com` / `*.example.com` | 同时匹配子域名（`example.com` 匹配 `www.example.com`，两种写法等价） |
| `ipcidr` | `10.0.0.0/8`、`127.0.0.0/8` | 命中 IP 字面量，也命中解析到该网段的**域名目标**（解析结果缓存 5 分钟） |
| `keyword` | `tracker` | 主机名包含该子串即命中 |

> 旧版规则文件中的 `cfip` 类型已移除（CF 网段功能整体删除），加载时自动跳过。

动作（即出站方式，**仅两种**）：

| 动作 | 含义 | 连接页「规则」列 |
| ---- | ---- | ---- |
| `direct` | 本地直连 | 直连 |
| `proxy` | 走 ECH 隧道 | 代理 |

> 旧版规则文件中的 `upstream` 动作已移除：加载时自动跳过并打警告日志，请改用 `direct` / `proxy`。

### 完整决策链

先按序匹配规则，未命中再走原版模式逻辑；每条连接只会走其中一条路：

| 顺序 | 条件 | 出站 | 失败后的处理 |
| ---- | ---- | ---- | ---- |
| 1 | 文件规则命中 `action=direct` | 直连 | 失败回 502 |
| 2 | 文件规则命中 `action=proxy` | ECH | 失败回 502 |
| 3 | 面板规则命中（任一动作） | 同 1~2 | 同 1~2 |
| 4 | 未命中，模式 `none` | 直连 | 失败回 502 |
| 5 | 未命中，模式 `global` / `custom` | ECH | 同 2 |
| 6 | 未命中，模式 `bypass_cn` 且目标是中国 IP | 直连 | 失败回 502 |
| 7 | 未命中，模式 `bypass_cn` 且目标是海外 IP / 解析失败 | ECH | 同 2 |

在 Web 面板「规则配置」页面添加，或编辑 `/data/rules.json`：

```
domain,baidu.com,direct
domain,google.com,proxy
domain,*.255432.xyz,proxy
ipcidr,10.0.0.0/8,direct
```

- `domain`：精确匹配 + 子域名匹配（`255432.xyz` 会匹配 `emos.255432.xyz`）
- `*.domain`：通配符写法，效果同上
- 自定义规则在**所有分流模式**下优先生效

## 部署后修改配置

**方式一：Web 面板**

打开 `http://你的IP:30001`，在「规则配置」页面修改后保存。

**方式二：编辑配置文件**

```bash
docker exec -it ech-proxy vi /data/config.json
docker restart ech-proxy
```

## 服务端说明

本项目只包含客户端。服务端为 Cloudflare Worker（`_worker.js`），将 WebSocket 流量转发到 ECH 入口。

项目内 `_worker.js` 为原版内容，仅供参考，部署时请使用你自己的 Worker 实例。

## 致谢

- 原项目：[byJoey/ech-wk](https://github.com/byJoey/ech-wk)
- 免费 ECH 节点：[Telegram @honghongtg](https://t.me/honghongtg)
- 中国 IP 列表：[mayaxcn/china-ip-list](https://github.com/mayaxcn/china-ip-list)
- 原始代码来源：[CF_NAT](https://t.me/CF_NAT)
