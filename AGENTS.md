# ech-wk-main — AGENTS.md

## 1. 项目简介与核心目标
ECH Workers 代理客户端的运行配置包：`config.json` 描述本地监听、服务端地址、分流模式、管理面板等。本目录无源码、无构建产物，只管"怎么配、怎么跑"。定位：原 ECH Proxy Panel 项目的便捷分流策略补充方案，原项目为主、本包为辅。

> 上游参考（只读，不在本目录改）：`../ech-proxy-panel-trae-agent-XS7r7t/README.md`（面板+参数全集）、`../ECHWorkers-windows-amd64/README.txt`（桌面端二进制说明）。字段默认值以 `ech-workers.go:217-228` 的 flag 定义为准。

## 2. 技术栈与关键依赖
- 无语言/框架依赖：本目录仅 JSON 配置。
- 运行时（二进制在外部，不在此目录）：`ech-workers`（Go）、或 Docker 镜像 `ghcr.io/dirige/ech-proxy-panel:latest`、或 `ECHWorkersGUI.exe`。
- 协议依赖：ECH + DoH（默认 `dns.alidns.com/dns-query`，ECH 域名默认 `cloudflare-ech.com`）。

## 3. 项目结构与架构说明
```
ech-wk-main/
├── config.json            # 现役配置（9 字段，见 README 配置表，可正常提交）
├── config.example.json    # 模板（可安全提交）
├── docker-compose.yml     # 一键启动示例（无密钥，可提交）
├── rules.example.txt      # custom 分流规则模板（用时复制为 rules.txt）
├── Dockerfile             # 镜像构建（基于 src/ 编译 ech-workers，CI 自动推 ghcr.io）
├── .github/workflows/     # CI：push main 自动构建+推送 Docker 镜像
├── scripts/
│   └── Test-Config.ps1    # 配置校验脚本（BOM+UTF-8，WinPS 5.1 可直接跑）
├── src/                   # Go 源码（ech-workers.go / index.html / chn_ip.txt 等，供 Docker 构建）
├── LICENSE                # MIT
├── README.md              # 人类使用说明
├── AGENTS.md              # 本文件
└── docs/                  # index / getting-started / faq-troubleshooting
```
数据流（按上游实现）：本地应用 → `listen_addr`（SOCKS5/HTTP）→ ECH 加密 → `server_addr`（经 `server_ip` 优选解析）→ 落地（`proxy_ip` 固定出口可选）→ 目标站。`routing_mode` 决定直连/代理。

## 4. 开发环境搭建 & 常用命令
本目录无 install/build/test/lint。唯一操作是校验 JSON 与启动外部程序：

```powershell
# 一键校验（运行目录：ech-wk-main）
.\scripts\Test-Config.ps1
# compose 启动（运行目录：ech-wk-main，需先备好 config.json）
docker compose up -d
# Docker 单条启动示例（按需替换占位符，运行目录：任意）
docker run -d --name ech-proxy --restart always -p 9090:9090 -p 30000:30000 -v ${PWD}/config.json:/data/config.json ghcr.io/dirige/ech-proxy-panel:latest
```

## 5. 代码风格与规范
- JSON 用 2 空格缩进，键顺序保持 `listen_addr → proxy_ip` 现状，新增键追加末尾。
- 地址格式：`listen_addr`/`server_addr`/`web_addr` 必须带端口；`server_ip` 允许 IP 或优选域名；`proxy_ip` 允许 `IP` 或 `IP:端口`（依据 `ech-workers.go:228`）。
- 文档示例可用现役值或占位符（`你的服务地址:443` / `your-token`）：本包 token/server_addr 系公开群组公开信息，无需保密、无需脱敏。
- `docker-compose.yml` 只挂载本地 `config.json` 路径；规则模板统一叫 `rules.example.txt`，用时复制改名为 `rules.txt`。

## 6. 测试要求
无自动化测试。改配置后必跑 `.\scripts\Test-Config.ps1`（覆盖：JSON 解析 + 9 字段齐全 + `server_addr` 含 `:443` + `routing_mode` 四值之一，见 `ech-workers.go:223` + 监听/面板端口不冲突），退出码非 0 即不通过。

## 7. Git 工作流与提交规范
- 本目录建议独立成仓或作为配置片段仓；无分支保护可依。
- 提交信息：`docs: ...` / `config: ...` 前缀，一行说清。
- `config.json` 含 token 但系公开群组公开信息，可正常提交；`config.example.json` 仅作模板保留。

## 8. 关键设计原则与约束
- 配置是唯一事实来源：改行为只改 `config.json`，不编造二进制参数。
- `routing_mode=custom` 时必须同步提供规则文件（`-rules`），否则 fallback 不可预期——用 `rules.example.txt` 作模板，复制改名后外部挂载（`docker-compose.yml` 已留好挂载注释行）。
- 端口唯一性：`listen_addr` 与 `web_addr` 不得冲突（当前 `30000` / `9090`）。

## 9. AI 代理行为守则
- 只改 `ech-wk-main/` 内文件；`../ech-proxy-panel-trae-agent-XS7r7t/` 与 `../ECHWorkers-windows-amd64/` 仅可读作参考，禁止修改。
- 不臆测缺失的二进制/源码存在位置；提到启动方式必须标注"二进制/镜像在外部"。
- token 等系公开群组公开信息，`.md` 中可直接引用现役值，无需脱敏。
- 不新增 `CLAUDE.md`（与本文件功能等价，只留一个）。

## 10. 常见陷阱 & 已知问题
- `server_ip` 填的是优选域名（`youxuan.cf.090227.xyz`）而非 IP：合法（`-ip` 支持域名），但该域名失效即全断，换 IP 时先 `nslookup` 验证。
- `proxy_ip` 裸 IP（无端口）合法，默认 443；若需指定端口才加 `:443`。
- 本目录 `routing_mode=bypass_cn` 与面板模板默认 `global` 不同：国内直连、国外代理；要全局代理需手动改 `global`。
- `web_addr=":9090"` 监听全接口且面板无鉴权：公网机必须收敛到 `127.0.0.1:9090` 或反代加鉴权；内网软路由场景才保留全接口。现役值保持全接口是为局域网开箱可用，改动前先确认使用环境。

## 11. 深入文档指针
- `docs/index.md` — 文档入口
- `docs/getting-started.md` — 9 字段详解 + 启动步骤
- `docs/faq-troubleshooting.md` — 端口占用/连不上/规则不生效排查

<!-- neat-freak: initialized at 2026-09-15 -->
