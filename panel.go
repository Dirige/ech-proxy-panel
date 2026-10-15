package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ======================== Web 管理面板（叠加层） ========================
//
// 本文件是「在原版 ech-workers.go 之上叠加」的全部面板实现（配置装配、分流规则、
// 连接/流量统计、Web 服务与 API、出口检测、每日定时重启）。
// 原版数据面保持逐字节不动，仅通过 ech-workers.go 内 4 处 [面板钩子 A~D] 接入：
//	A = main 中 flag.Parse() 后调用 panelStartup()（本文件）
//	B = main 中分流模式分支补 custom（避免被原版重置为 global）
//	C = handleTunnel 分流决策入口（panelTunnelBegin/End，见下）
//	D = 拨通 WS 后记录实拨地址（panelTunnel.setDialed，见下）
//
// 参数优先级（原版 flag 语义为王）：
//	命令行(flag.Visit 显式传入) > 环境变量 ECH_* > 配置文件 > 原版 flag 默认值
//
// 文件内大致分段（自上而下）：
//	配置装配/持久化/校验 → 分流规则 → 连接与流量统计/日志缓冲（含钩子 C/D）
//	→ Web 面板与 API（内嵌 index.html）→ 出口 IP 检测 → 每日定时自重启

// appVersion 面板与镜像版本号（发版时同步改这里 + workflow tag）
const appVersion = "1.6"

const (
	// defaultConfigPath 配置文件默认路径（flag 默认值）
	defaultConfigPath = "/data/config.json"
	// builtinConfigPath 镜像内置配置：仅当用户配置文件不存在时兜底，
	// 保证 Docker 出箱可用；用户一旦有自己的配置文件则完全以其为准
	builtinConfigPath = "/etc/ech/config.json"
)

var (
	webAddr      string // Web 管理面板监听地址
	rulesFile    string // 自定义规则文件路径（只读桶）
	rulesData    string // 面板规则持久化文件路径（可编辑桶）
	configFile   string // 配置文件路径
	webPassword  string // Web 管理面板登录密码（为空则不需登录）
	restartAtRaw string // 每日定时重启时间 (HH:MM，off=关闭)

	// Web 会话管理
	webSessions   map[string]time.Time // sessionID -> 过期时间
	webSessionsMu sync.Mutex
)

// init 注册面板侧参数（原版 init() 里的 7 个原版参数保持不动）
func init() {
	flag.StringVar(&webAddr, "web", "", "Web 管理面板监听地址 (如 :30001)")
	flag.StringVar(&rulesFile, "rules", "", "自定义规则文件路径 (type,value,action CSV，只读)")
	flag.StringVar(&rulesData, "rules-data", "/data/rules.json", "面板规则持久化文件路径")
	flag.StringVar(&configFile, "config", defaultConfigPath, "配置文件路径")
	flag.StringVar(&webPassword, "password", "", "Web 管理面板登录密码 (为空则不需登录)")
	flag.StringVar(&restartAtRaw, "restart-at", "05:00", "每日定时重启 (本地时区 HH:MM，off=关闭，进程内自重启不中断容器)")
}

// panelStartup [面板钩子 A] 面板启动装配，在原版 main 的 flag.Parse() 之后调用一次。
// 只做「参数合并 + 面板服务启动」，原版 main 的 serverAddr 校验、prepareECH、
// 中国 IP 加载、runProxyServer 流程一律不动。
func panelStartup() {
	// 0) 最先挂日志捕获：本函数及其后的所有日志都进面板缓冲
	startTime = time.Now()
	logBuffer = make([]logEntry, 0, 200)
	dnsCache = make(map[string]dnsCacheEntry)
	webSessions = make(map[string]time.Time)
	log.SetOutput(&logWriter{original: os.Stderr})

	// 1) 命令行显式传入的参数（最高优先级）
	cliSet := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { cliSet[f.Name] = true })

	// 2) 环境变量：只作用于未显式传参的 flag
	envSet := make(map[string]bool)
	applyEnv := func(name, key string, dst *string) {
		if cliSet[name] {
			return
		}
		if v := os.Getenv(key); v != "" {
			*dst = v
			envSet[name] = true
		}
	}
	applyEnv("l", "ECH_LISTEN", &listenAddr)
	applyEnv("f", "ECH_SERVER", &serverAddr)
	applyEnv("ip", "ECH_SERVER_IP", &serverIP)
	applyEnv("token", "ECH_TOKEN", &token)
	applyEnv("dns", "ECH_DNS", &dnsServer)
	applyEnv("ech", "ECH_DOMAIN", &echDomain)
	applyEnv("routing", "ECH_ROUTING", &routingMode)
	applyEnv("web", "ECH_WEB", &webAddr)
	applyEnv("password", "ECH_PASSWORD", &webPassword)
	applyEnv("config", "ECH_CONFIG", &configFile)
	applyEnv("rules-data", "ECH_RULES_DATA", &rulesData)
	applyEnv("restart-at", "ECH_RESTART_AT", &restartAtRaw)

	// 3) 配置文件：只填充「命令行与环境变量都没提供」的项。
	//    用户配置不存在时回退镜像内置配置（Docker 出箱可用），自己的配置永远优先。
	cfg, err := loadConfig(configFile)
	if err != nil && configFile == defaultConfigPath {
		if b, berr := loadConfig(builtinConfigPath); berr == nil {
			cfg, err = b, nil
			log.Printf("[启动] 配置文件 %s 不存在，使用镜像内置配置: %s", configFile, builtinConfigPath)
		}
	}
	if err == nil {
		log.Printf("[启动] 已加载配置文件: %s", configFile)
		applyCfg := func(name string, dst *string, val string) {
			if cliSet[name] || envSet[name] || val == "" {
				return
			}
			*dst = val
		}
		applyCfg("l", &listenAddr, cfg.ListenAddr)
		applyCfg("f", &serverAddr, cfg.ServerAddr)
		applyCfg("ip", &serverIP, cfg.ServerIP)
		applyCfg("token", &token, cfg.Token)
		applyCfg("dns", &dnsServer, cfg.DNSServer)
		applyCfg("ech", &echDomain, cfg.ECHDomain)
		applyCfg("routing", &routingMode, cfg.RoutingMode)
		applyCfg("web", &webAddr, cfg.WebAddr)
		applyCfg("password", &webPassword, cfg.WebPassword)
	} else if configFile != "" {
		log.Printf("[启动] 配置文件不存在或无效，使用命令行/环境变量参数")
	}

	// 4) 没有配置文件时把当前参数落盘，供面板在线修改
	if configFile != "" {
		if _, serr := os.Stat(configFile); os.IsNotExist(serr) && serverAddr != "" {
			if err := saveConfig(configFile); err != nil {
				log.Printf("[启动] 保存配置文件失败: %v", err)
			} else {
				log.Printf("[启动] 已保存配置到: %s", configFile)
			}
		}
	}

	// 5) 面板地址：默认 :30001，宽容输入（纯端口补 ":"），非法回退避免面板静默死亡
	if webAddr == "" {
		webAddr = ":30001"
		log.Printf("[启动] 未指定面板地址，默认监听 :30001")
	}
	webAddr = normalizeWebAddr(webAddr)
	if !validHostPort(webAddr) {
		log.Printf("[警告] web_addr 无效 (%s)，回退为 :30001", webAddr)
		webAddr = ":30001"
	}

	// 6) 加载自定义规则：-rules CSV（只读桶） + 面板 rules.json（可编辑桶）
	if rulesFile != "" {
		if err := loadCustomRules(rulesFile); err != nil {
			log.Printf("[警告] 加载自定义规则失败: %v", err)
		}
	}
	loadRulesDataFile()

	// 7) 启动面板服务
	startExitInfoDetector()    // 出口 IP：启动 5 秒后探测一次，之后按需检测
	go startWebServer(webAddr) // Web 管理面板
	go startScheduledRestart() // 每日定时自重启（默认 05:00，off 关闭）

	// 8) 信号处理（原版无此逻辑，进程被强杀时行为与原版一致）
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("[退出] 收到终止信号，正在关闭...")
		os.Exit(0)
	}()
}

// ======================== 配置持久化 ========================

// appConfig 应用配置
type appConfig struct {
	ListenAddr  string `json:"listen_addr"`
	ServerAddr  string `json:"server_addr"`
	ServerIP    string `json:"server_ip"`
	Token       string `json:"token"`
	DNSServer   string `json:"dns_server"`
	ECHDomain   string `json:"ech_domain"`
	RoutingMode string `json:"routing_mode"`
	WebAddr     string `json:"web_addr"`
	WebPassword string `json:"web_password"`
}

// loadConfig 从文件加载配置
func loadConfig(filePath string) (*appConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var cfg appConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// saveConfig 保存配置到文件
func saveConfig(filePath string) error {
	cfg := appConfig{
		ListenAddr:  listenAddr,
		ServerAddr:  serverAddr,
		ServerIP:    serverIP,
		Token:       token,
		DNSServer:   dnsServer,
		ECHDomain:   echDomain,
		RoutingMode: routingMode,
		WebAddr:     webAddr,
		WebPassword: webPassword,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(filePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建配置目录失败: %w", err)
		}
	}
	return os.WriteFile(filePath, data, 0600)
}

// ======================== 地址校验辅助 ========================

// validHostPort 校验 host:port 格式（:30001、0.0.0.0:30000、example.com:443 均合法）
func validHostPort(s string) bool {
	_, port, err := net.SplitHostPort(s)
	return err == nil && port != ""
}

// normalizeWebAddr 面板地址宽容输入：纯端口（如 "30001"）补成 ":30001"，其余原样返回
func normalizeWebAddr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if _, err := strconv.Atoi(s); err == nil {
		return ":" + s
	}
	return s
}

// 分流规则（direct/proxy 两桶）、DNS 解析缓存与分流决策。
// 规则在所有分流模式下优先于模式生效；未命中规则时回落原版 shouldBypassProxy。

// customRule 自定义规则
type customRule struct {
	Type   string `json:"type"`   // domain, ipcidr, keyword
	Value  string `json:"value"`  // 规则值（domain 支持通配符 *.example.com）
	Action string `json:"action"` // proxy(ECH) 或 direct
}

var (
	// 自定义规则（两个来源分桶，互不覆盖：-rules CSV 只读、面板 rules.json 可编辑）
	// customRules 为生效列表，顺序固定：fileRules 优先，panelRules 其后
	customRules   []customRule
	fileRules     []customRule
	panelRules    []customRule
	customRulesMu sync.RWMutex

	// DNS 解析结果缓存（规则 ipcidr 匹配域名时避免重复查询）
	dnsCache   map[string]dnsCacheEntry
	dnsCacheMu sync.RWMutex

	// 中国 IP 列表补加载互斥（启动时由原版 main 加载，运行时切模式走 ensureChinaIPs）
	chinaEnsureMu sync.Mutex
)

// dnsCacheEntry DNS 缓存条目
type dnsCacheEntry struct {
	ips       []net.IP
	expiresAt time.Time
}

// ======================== 分流决策 ========================

// outboundKind 出站类型：仅直连与 ECH 两种出站
type outboundKind int

const (
	outDirect outboundKind = iota // 本地直连
	outECH                        // ECH 隧道
)

// ruleActionToOutbound 规则动作 → 出站类型（proxy 缺省按 ECH 处理）
func ruleActionToOutbound(action string) outboundKind {
	if action == "direct" {
		return outDirect
	}
	return outECH
}

// routeDecision 分流决策：自定义规则优先级最高（任何分流模式下都生效），
// 未命中规则时按原版分流模式（global/bypass_cn/none/custom）决定；
// 同时返回命中依据（面板「命中」列）与命中的规则（非 nil 时打 [规则] 日志）。
func routeDecision(targetHost string) (out outboundKind, matched string, rule *customRule) {
	// 1. 自定义规则（直连列表 / 代理列表）
	if hit, r, out2 := matchCustomRule(targetHost); hit {
		rr := r
		if out2 == outDirect {
			return outDirect, fmt.Sprintf("规则 %s:%s → 直连", r.Type, r.Value), &rr
		}
		return outECH, fmt.Sprintf("规则 %s:%s → 代理", r.Type, r.Value), &rr
	}

	// 2. 未命中规则：按分流模式（原版 shouldBypassProxy 逻辑不变）
	if shouldBypassProxy(targetHost) {
		return outDirect, fmt.Sprintf("模式 %s → 直连", routingMode), nil
	}
	return outECH, fmt.Sprintf("模式 %s → 代理", routingMode), nil
}

// matchCustomRule 匹配自定义规则，返回 (是否命中, 命中的规则, 出站类型)
func matchCustomRule(targetHost string) (bool, customRule, outboundKind) {
	customRulesMu.RLock()
	defer customRulesMu.RUnlock()

	if len(customRules) == 0 {
		return false, customRule{}, outECH
	}

	host := strings.ToLower(strings.TrimSpace(targetHost))
	// 检查是否是IP地址
	isIP := net.ParseIP(host) != nil

	// ipcidr 规则对域名目标同样生效——按规则顺序遍历，遇到第一条需要 IP 的规则时才解析一次
	var resolvedIPs []net.IP
	resolved := false
	resolveOnce := func() {
		if resolved {
			return
		}
		resolved = true
		if ips, err := lookupIPWithCache(host); err == nil {
			resolvedIPs = ips
		}
	}

	for _, rule := range customRules {
		switch rule.Type {
		case "domain":
			if !isIP {
				value := strings.ToLower(strings.TrimSpace(rule.Value))
				matched := false
				// 支持通配符 *.domain
				if strings.HasPrefix(value, "*.") {
					suffix := strings.TrimPrefix(value, "*.")
					if host == suffix || strings.HasSuffix(host, "."+suffix) {
						matched = true
					}
				} else {
					// 精确匹配或子域名匹配
					if host == value || strings.HasSuffix(host, "."+value) {
						matched = true
					}
				}
				if matched {
					return true, rule, ruleActionToOutbound(rule.Action)
				}
			}
		case "ipcidr":
			_, cidr, err := net.ParseCIDR(rule.Value)
			if err != nil {
				continue
			}
			if isIP {
				if ip := net.ParseIP(host); ip != nil && cidr.Contains(ip) {
					return true, rule, ruleActionToOutbound(rule.Action)
				}
				continue
			}
			// 域名目标：解析后回查（解析结果由 lookupIPWithCache 缓存 5 分钟）
			resolveOnce()
			for _, ip := range resolvedIPs {
				if cidr.Contains(ip) {
					return true, rule, ruleActionToOutbound(rule.Action)
				}
			}
		case "keyword":
			if rule.Value != "" && strings.Contains(host, strings.ToLower(rule.Value)) {
				return true, rule, ruleActionToOutbound(rule.Action)
			}
		}
	}
	// 没有匹配规则
	return false, customRule{}, outECH
}

// ======================== DNS 解析缓存 ========================

// lookupIPWithCache 带缓存的 DNS 查询（规则 ipcidr 匹配域名时用，缓存 5 分钟）
func lookupIPWithCache(host string) ([]net.IP, error) {
	dnsCacheMu.RLock()
	entry, ok := dnsCache[host]
	dnsCacheMu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.ips, nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}

	dnsCacheMu.Lock()
	dnsCache[host] = dnsCacheEntry{
		ips:       ips,
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	// 简单清理：超过 1000 条时清空
	if len(dnsCache) > 1000 {
		for k := range dnsCache {
			delete(dnsCache, k)
		}
	}
	dnsCacheMu.Unlock()

	return ips, nil
}

// ======================== 规则加载与持久化 ========================

// parseRulesFile 解析规则文件（CSV：type,value,action），返回解析出的规则
func parseRulesFile(filePath string) ([]customRule, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("打开规则文件失败: %w", err)
	}
	defer file.Close()

	var rules []customRule
	skippedUpstream := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		parts := strings.SplitN(line, ",", 3)
		if len(parts) != 3 {
			continue
		}

		ruleType := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		action := strings.TrimSpace(parts[2])

		if ruleType != "domain" && ruleType != "ipcidr" && ruleType != "keyword" {
			continue
		}
		if action == "upstream" { // 该动作已移除（仅保留 direct/proxy 两种出站）
			skippedUpstream++
			continue
		}
		if action != "proxy" && action != "direct" {
			continue
		}
		if value == "" {
			continue
		}

		rules = append(rules, customRule{
			Type:   ruleType,
			Value:  value,
			Action: action,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取规则文件失败: %w", err)
	}

	if skippedUpstream > 0 {
		log.Printf("[规则] %s 中 %d 条 upstream 动作规则已跳过（该动作已移除，请改用 direct/proxy）", filePath, skippedUpstream)
	}

	return rules, nil
}

// rebuildCustomRulesLocked 重建生效规则列表（调用方需已持有 customRulesMu 写锁）
func rebuildCustomRulesLocked() {
	merged := make([]customRule, 0, len(fileRules)+len(panelRules))
	merged = append(merged, fileRules...)
	merged = append(merged, panelRules...)
	customRules = merged
}

// loadCustomRules 从 -rules 指定的 CSV 文件加载规则（只读桶，面板保存不会覆盖它）
func loadCustomRules(filePath string) error {
	rules, err := parseRulesFile(filePath)
	if err != nil {
		return err
	}

	customRulesMu.Lock()
	fileRules = rules
	rebuildCustomRulesLocked()
	total := len(customRules)
	customRulesMu.Unlock()

	log.Printf("[规则] 已加载文件规则 %d 条 (%s)，生效规则合计 %d 条", len(rules), filePath, total)
	return nil
}

// saveCustomRules 保存面板规则到文件（只写面板桶，避免把 -rules CSV 规则抄进 rules.json）
func saveCustomRules(filePath string) error {
	customRulesMu.RLock()
	rules := make([]customRule, len(panelRules))
	copy(rules, panelRules)
	customRulesMu.RUnlock()

	// 确保目录存在
	if dir := filepath.Dir(filePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建规则目录失败: %w", err)
		}
	}

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("创建规则文件失败: %w", err)
	}
	defer file.Close()

	for _, rule := range rules {
		fmt.Fprintf(file, "%s,%s,%s\n", rule.Type, rule.Value, rule.Action)
	}

	return nil
}

// loadRulesDataFile 从面板规则持久化文件加载规则（可编辑桶，与 -rules CSV 分开存放）
func loadRulesDataFile() {
	if rulesData == "" {
		return
	}
	if _, err := os.Stat(rulesData); os.IsNotExist(err) {
		return
	}
	rules, err := parseRulesFile(rulesData)
	if err != nil {
		log.Printf("[规则] 加载面板规则失败: %v", err)
		return
	}

	customRulesMu.Lock()
	panelRules = rules
	rebuildCustomRulesLocked()
	fileCnt, panelCnt, total := len(fileRules), len(panelRules), len(customRules)
	customRulesMu.Unlock()

	log.Printf("[规则] 已加载面板规则 %d 条（文件规则 %d 条，生效合计 %d 条）", panelCnt, fileCnt, total)
}

// ======================== 中国 IP 列表（运行时补加载） ========================

// ensureChinaIPs 确保中国 IP 列表已加载。启动时模式即为 bypass_cn 的情况由原版
// main 负责加载（原版逻辑不动）；面板在运行时把分流模式切到 bypass_cn 时调用这里补加载。
func ensureChinaIPs() {
	chinaEnsureMu.Lock()
	defer chinaEnsureMu.Unlock()

	chinaIPRangesMu.RLock()
	n4 := len(chinaIPRanges)
	chinaIPRangesMu.RUnlock()
	chinaIPV6RangesMu.RLock()
	n6 := len(chinaIPV6Ranges)
	chinaIPV6RangesMu.RUnlock()
	if n4 > 0 || n6 > 0 {
		return
	}

	log.Printf("[启动] 分流模式: 跳过中国大陆，正在加载中国IP列表...")
	if err := loadChinaIPList(); err != nil {
		log.Printf("[警告] 加载中国IPv4列表失败: %v", err)
	}
	if err := loadChinaIPV6List(); err != nil {
		log.Printf("[警告] 加载中国IPv6列表失败: %v", err)
	}

	chinaIPRangesMu.RLock()
	n4 = len(chinaIPRanges)
	chinaIPRangesMu.RUnlock()
	chinaIPV6RangesMu.RLock()
	n6 = len(chinaIPV6Ranges)
	chinaIPV6RangesMu.RUnlock()
	if n4 > 0 || n6 > 0 {
		log.Printf("[启动] 已加载 %d 个中国IPv4段, %d 个中国IPv6段", n4, n6)
	} else {
		log.Printf("[警告] 未加载到任何中国IP列表，bypass_cn 模式下海外判定将失效（全部走代理）")
	}
}

// 连接追踪、流量统计、日志缓冲，以及原版 handleTunnel 中 [面板钩子 C/D] 的实现。

// ======================== 日志缓冲 ========================

// logWriter 日志写出器：写 stderr 的同时进入缓冲（面板概览页日志面板读取）
type logWriter struct {
	original io.Writer
}

func (w *logWriter) Write(p []byte) (int, error) {
	func() {
		defer func() {
			if r := recover(); r != nil {
				w.original.Write([]byte(fmt.Sprintf("[logWriter panic: %v]\n", r)))
			}
		}()
		msg := strings.TrimSpace(string(p))
		if msg != "" {
			level := "info"
			if strings.Contains(msg, "[警告]") || strings.Contains(msg, "warn") {
				level = "warn"
			} else if strings.Contains(msg, "[错误]") || strings.Contains(msg, "fatal") || strings.Contains(msg, "error") {
				level = "error"
			}
			addLog(level, msg)
		}
	}()
	return w.original.Write(p)
}

// addLog 添加日志到缓冲
func addLog(level, msg string) {
	entry := logEntry{
		Time:  time.Now().Format("2006-01-02 15:04:05"),
		Level: level,
		Msg:   msg,
	}
	logBufferMu.Lock()
	logBuffer = append(logBuffer, entry)
	if len(logBuffer) > 200 {
		logBuffer = logBuffer[len(logBuffer)-200:]
	}
	logBufferMu.Unlock()
}

// ======================== 连接追踪与流量统计 ========================

var (
	// 连接追踪
	activeConns   sync.Map // key: connID(string) -> *connInfo
	connIDCounter atomic.Int64
	activeConnCnt atomic.Int64 // 活跃连接数（避免全量遍历）

	// 流量统计
	totalUpload   atomic.Int64
	totalDownload atomic.Int64
	totalConns    atomic.Int64
	startTime     time.Time

	// 日志缓冲 (最近 200 条)
	logBuffer   []logEntry
	logBufferMu sync.Mutex
)

// connInfo 连接信息
type connInfo struct {
	ID        string
	Source    string
	Target    string
	Mode      string
	Rule      string
	Matched   string // 命中依据（哪条规则 / 哪个分流模式），面板可观测
	StartTime time.Time
	dialed    atomic.Value // string：实际拨通地址（连接早期写、接口并发读，原子化避免数据竞争）
	upload    atomic.Int64
	download  atomic.Int64
}

// connInfoResp 用于 JSON 序列化的连接信息（不含 atomic 字段）
type connInfoResp struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Target     string    `json:"target"`
	Mode       string    `json:"mode"`
	Rule       string    `json:"rule"`
	Matched    string    `json:"matched"`
	DialedAddr string    `json:"dialed_addr"`
	Upload     int64     `json:"upload"`
	Download   int64     `json:"download"`
	StartTime  time.Time `json:"start_time"`
}

// logEntry 日志条目
type logEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// setDialed 记录实际拨通的远端地址（nil 安全）
func (c *connInfo) setDialed(addr string) {
	if c != nil && addr != "" {
		c.dialed.Store(addr)
	}
}

// getDialed 读取实际拨通的远端地址（nil 安全）
func (c *connInfo) getDialed() string {
	if c == nil {
		return ""
	}
	s, _ := c.dialed.Load().(string)
	return s
}

// addConn 添加连接追踪
func addConn(source, target, mode, rule, matched string) *connInfo {
	id := fmt.Sprintf("%d", connIDCounter.Add(1))
	info := &connInfo{
		ID:        id,
		Source:    source,
		Target:    target,
		Mode:      mode,
		Rule:      rule,
		Matched:   matched,
		StartTime: time.Now(),
	}
	activeConns.Store(id, info)
	activeConnCnt.Add(1)
	totalConns.Add(1)
	return info
}

// removeConn 移除连接追踪，将流量计入总统计
func removeConn(id string) {
	if v, ok := activeConns.LoadAndDelete(id); ok {
		activeConnCnt.Add(-1)
		info := v.(*connInfo)
		totalUpload.Add(info.upload.Load())
		totalDownload.Add(info.download.Load())
	}
}

// getActiveConns 获取所有活跃连接
func getActiveConns() []connInfoResp {
	var conns []connInfoResp
	activeConns.Range(func(key, value interface{}) bool {
		info := value.(*connInfo)
		conns = append(conns, connInfoResp{
			ID:         info.ID,
			Source:     info.Source,
			Target:     info.Target,
			Mode:       info.Mode,
			Rule:       info.Rule,
			Matched:    info.Matched,
			DialedAddr: info.getDialed(),
			Upload:     info.upload.Load(),
			Download:   info.download.Load(),
			StartTime:  info.StartTime,
		})
		return true
	})
	return conns
}

// ======================== 原版数据面挂钩（钩子 C/D） ========================

// panelTunnel 原版 handleTunnel 内的面板上下文（[面板钩子 C/D] 使用）
type panelTunnel struct {
	direct bool      // true = 走原版直连分支
	info   *connInfo // 连接统计记录（nil 安全）
}

// panelTunnelBegin [面板钩子 C] 连接入口：
// 分流决策（自定义规则优先，未命中回落原版 shouldBypassProxy）+ 登记连接 +
// 返回包装后的 conn（读=上传、写=下载），交回原版流程继续处理。
func panelTunnelBegin(conn net.Conn, clientAddr, target, targetHost string, mode int) (net.Conn, *panelTunnel) {
	// 清除原版 handleConnection 的30秒"握手限时"deadline（绝对时间）。
	// 原版只有代理分支在拨通 WS 后清除它，直连分支从不清除，导致所有直连连接
	//（bypass_cn 国内站点、direct 规则）在建立30秒后被硬断——传输中也照断，
	// 表现为"视频缓冲完成即断连、看完缓存要重新连接重新缓冲"。
	// 至此 SOCKS5/HTTP 握手已完成（未完成握手的连接到不了这里，限时依然生效），
	// 两条分支都不再需要它；代理分支原版随后的同款清除语句保持不变，重复清除无害。
	conn.SetDeadline(time.Time{})

	out, matched, rule := routeDecision(targetHost)

	modeStr := "socks5"
	if mode == modeHTTPConnect {
		modeStr = "http-connect"
	} else if mode == modeHTTPProxy {
		modeStr = "http-proxy"
	}
	ruleStr := "proxy"
	if out == outDirect {
		ruleStr = "direct"
	}

	info := addConn(clientAddr, target, modeStr, ruleStr, matched)
	if rule != nil {
		// 自定义规则命中单独记一条日志（原版 [分流] 日志不含依据，保持原样）
		log.Printf("[规则] %s → %s（命中 %s:%s）", target, ruleStr, rule.Type, rule.Value)
	}

	return &countingConn{Conn: conn, info: info}, &panelTunnel{direct: out == outDirect, info: info}
}

// panelTunnelEnd [面板钩子 C] 连接结束：移出追踪并把字节并入总量
func panelTunnelEnd(t *panelTunnel) {
	if t == nil || t.info == nil {
		return
	}
	removeConn(t.info.ID)
}

// setDialed [面板钩子 D] 记录实际拨通地址（nil 安全）
func (t *panelTunnel) setDialed(addr string) {
	if t == nil || t.info == nil {
		return
	}
	t.info.setDialed(addr)
}

// countingConn 客户端连接统计包装：Read 计入上传、Write 计入下载；
// 其余 net.Conn 方法（Close/Deadline/RemoteAddr…）经嵌入原样透传，行为与原 conn 一致。
type countingConn struct {
	net.Conn
	info *connInfo
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && c.info != nil {
		c.info.upload.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 && c.info != nil {
		c.info.download.Add(int64(n))
	}
	return n, err
}

// Web 管理面板：HTTP 服务、登录鉴权、四页（概览/连接/流量/规则配置）API 与 WebSocket 实时推送。

//go:embed index.html
var indexHTML embed.FS

// statusResponse 状态响应
type statusResponse struct {
	Version       string  `json:"version"`
	Uptime        string  `json:"uptime"`
	TotalConns    int64   `json:"total_conns"`
	ActiveConns   int     `json:"active_conns"`
	TotalUpload   int64   `json:"total_upload"`
	TotalDownload int64   `json:"total_download"`
	RoutingMode   string  `json:"routing_mode"`
	ListenAddr    string  `json:"listen_addr"`
	ServerAddr    string  `json:"server_addr"`
	ECHDomain     string  `json:"ech_domain"`
	WebAddr       string  `json:"web_addr"`
	CustomRules   int     `json:"custom_rules"`
	MemoryMB      float64 `json:"memory_mb"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// startWebServer 启动 Web 管理面板
func startWebServer(addr string) {
	mux := http.NewServeMux()

	// 登录/登出（不需鉴权）
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/api/login", handleAPILogin)
	mux.HandleFunc("/api/logout", handleAPILogout)

	// 需要鉴权的路由
	mux.Handle("/", authMiddleware(http.HandlerFunc(handleIndex)))
	mux.Handle("/api/status", authMiddleware(http.HandlerFunc(handleStatus)))
	mux.Handle("/api/config", authMiddleware(http.HandlerFunc(handleConfig)))
	mux.Handle("/api/rules", authMiddleware(http.HandlerFunc(handleRules)))
	mux.Handle("/api/connections", authMiddleware(http.HandlerFunc(handleConnections)))
	mux.Handle("/api/traffic", authMiddleware(http.HandlerFunc(handleTraffic)))
	mux.Handle("/api/logs", authMiddleware(http.HandlerFunc(handleLogs)))
	mux.Handle("/api/service/", authMiddleware(http.HandlerFunc(handleService)))
	mux.Handle("/api/exit-info", authMiddleware(http.HandlerFunc(handleExitInfo)))
	mux.Handle("/ws", authMiddleware(http.HandlerFunc(handleWebSocket)))

	log.Printf("[Web] 管理面板启动: http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("[Web] 管理面板启动失败: %v", err)
	}
}

// authMiddleware 鉴权中间件（webPassword 为空时跳过鉴权）
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if webPassword == "" {
			next.ServeHTTP(w, r)
			return
		}

		// 检查 cookie 中的 session
		if cookie, err := r.Cookie("ech_session"); err == nil {
			webSessionsMu.Lock()
			if exp, ok := webSessions[cookie.Value]; ok && time.Now().Before(exp) {
				webSessionsMu.Unlock()
				next.ServeHTTP(w, r)
				return
			}
			delete(webSessions, cookie.Value)
			webSessionsMu.Unlock()
		}

		// 未登录，API 返回 401，页面重定向到登录页
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// handleLogin 提供登录页面
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if webPassword == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginHTML))
}

// loginHTML 登录页面
var loginHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ECH Proxy - 登录</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0f1117;color:#e1e4e8;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh}
.login-box{background:#161b22;border:1px solid #30363d;border-radius:12px;padding:32px;width:360px;box-shadow:0 8px 24px rgba(0,0,0,.4)}
.login-box h2{text-align:center;margin-bottom:24px;color:#58a6ff;font-size:1.3em}
.login-box input{width:100%;padding:10px 14px;background:#0d1117;border:1px solid #30363d;border-radius:8px;color:#e1e4e8;font-size:14px;margin-bottom:16px;outline:none}
.login-box input:focus{border-color:#58a6ff}
.login-box button{width:100%;padding:10px;background:#238636;color:#fff;border:none;border-radius:8px;font-size:14px;cursor:pointer;font-weight:600}
.login-box button:hover{background:#2ea043}
.error{color:#f85149;text-align:center;margin-bottom:12px;font-size:13px;display:none}
</style>
</head>
<body>
<div class="login-box">
<h2>ECH Proxy 管理面板</h2>
<div class="error" id="err"></div>
<input type="password" id="pw" placeholder="请输入管理密码" autofocus>
<button onclick="doLogin()">登 录</button>
</div>
<script>
document.getElementById('pw').addEventListener('keydown',e=>{if(e.key==='Enter')doLogin()});
async function doLogin(){
const pw=document.getElementById('pw').value;
const err=document.getElementById('err');
try{
const r=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:pw})});
const d=await r.json();
if(d.status==='ok'){location.href='/'}else{err.textContent=d.error||'密码错误';err.style.display='block'}
}catch(e){err.textContent='网络错误';err.style.display='block'}
}
</script>
</body>
</html>`

// handleAPILogin 处理登录请求
func handleAPILogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "请求格式错误"})
		return
	}
	if req.Password != webPassword {
		json.NewEncoder(w).Encode(map[string]string{"error": "密码错误"})
		return
	}
	// 生成 session
	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())
	webSessionsMu.Lock()
	webSessions[sessionID] = time.Now().Add(7 * 24 * time.Hour)
	webSessionsMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "ech_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   7 * 24 * 3600,
	})
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleAPILogout 退出登录
func handleAPILogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("ech_session"); err == nil {
		webSessionsMu.Lock()
		delete(webSessions, cookie.Value)
		webSessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:   "ech_session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// handleIndex 提供 index.html
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := indexHTML.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Internal Server Error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// handleStatus 获取系统状态
func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	customRulesMu.RLock()
	customRulesCount := len(customRules)
	customRulesMu.RUnlock()
	json.NewEncoder(w).Encode(statusResponse{
		Version:       appVersion,
		Uptime:        time.Since(startTime).Truncate(time.Second).String(),
		TotalConns:    totalConns.Load(),
		ActiveConns:   int(activeConnCnt.Load()),
		TotalUpload:   totalUpload.Load(),
		TotalDownload: totalDownload.Load(),
		RoutingMode:   routingMode,
		ListenAddr:    listenAddr,
		ServerAddr:    serverAddr,
		ECHDomain:     echDomain,
		WebAddr:       webAddr,
		CustomRules:   customRulesCount,
		MemoryMB:      float64(m.Alloc) / 1024 / 1024,
	})
}

// handleConfig 获取/更新配置
func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "GET" {
		config := map[string]interface{}{
			"listen_addr":  listenAddr,
			"server_addr":  serverAddr,
			"server_ip":    serverIP,
			"token":        token,
			"dns_server":   dnsServer,
			"ech_domain":   echDomain,
			"routing_mode": routingMode,
			"web_addr":     webAddr,
			"rules_file":   rulesFile,
			"web_password": webPassword,
		}
		json.NewEncoder(w).Encode(config)
		return
	}

	// POST: 更新配置
	var update map[string]string
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, `{"error":"invalid json"}`, 400)
		return
	}

	// 校验地址类字段：非法值直接拒绝，避免写坏配置导致面板/代理无法启动
	if v, ok := update["listen_addr"]; ok && v != "" && !validHostPort(v) {
		json.NewEncoder(w).Encode(map[string]string{"error": "listen_addr 非法，需要 host:port 格式，例如 0.0.0.0:30000"})
		return
	}
	if v, ok := update["server_addr"]; ok && v != "" && !validHostPort(v) {
		json.NewEncoder(w).Encode(map[string]string{"error": "server_addr 非法，需要 host:port 格式，例如 xxx.workers.dev:443"})
		return
	}
	// 面板地址宽容：纯端口输入（如 30001）先归一化成 :30001 再校验
	if v, ok := update["web_addr"]; ok {
		update["web_addr"] = normalizeWebAddr(v)
	}
	if v, ok := update["web_addr"]; ok && v != "" && !validHostPort(v) {
		json.NewEncoder(w).Encode(map[string]string{"error": "web_addr 非法，可填端口（如 30001）或 host:port（如 0.0.0.0:30001）"})
		return
	}
	if v, ok := update["dns_server"]; ok && v == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "dns_server 不能为空"})
		return
	}
	if v, ok := update["ech_domain"]; ok && v == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "ech_domain 不能为空"})
		return
	}

	if v, ok := update["listen_addr"]; ok && v != "" {
		listenAddr = v
	}
	if v, ok := update["server_addr"]; ok && v != "" {
		serverAddr = v
	}
	if v, ok := update["server_ip"]; ok {
		serverIP = v
	}
	if v, ok := update["token"]; ok && v != "" {
		token = v
	}
	if v, ok := update["dns_server"]; ok && v != "" {
		dnsServer = v
	}
	if v, ok := update["ech_domain"]; ok && v != "" {
		echDomain = v
	}
	if v, ok := update["web_password"]; ok {
		webPassword = v
		log.Printf("[配置] 面板密码已更新")
	}
	if v, ok := update["routing_mode"]; ok && v != "" {
		switch v {
		case "global", "bypass_cn", "none", "custom":
			routingMode = v
			if routingMode == "bypass_cn" {
				// 原版只在启动时按需加载中国 IP 列表，运行时切过来要补加载
				ensureChinaIPs()
			}
			log.Printf("[配置] 分流模式已更新: %s", routingMode)
		}
	}
	if v, ok := update["web_addr"]; ok && v != "" {
		webAddr = v
	}

	// 刷新 ECH
	if err := refreshECH(); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 保存配置到文件
	if err := saveConfig(configFile); err != nil {
		log.Printf("[配置] 保存配置文件失败: %v", err)
	} else {
		log.Printf("[配置] 配置已保存到: %s", configFile)
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleRules 获取/更新自定义规则
func handleRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "GET" {
		// 只返回面板桶：-rules CSV 属于只读配置，面板改不到它（规则源分离）
		customRulesMu.RLock()
		rules := make([]customRule, len(panelRules))
		copy(rules, panelRules)
		customRulesMu.RUnlock()
		json.NewEncoder(w).Encode(rules)
		return
	}

	// POST: 替换面板规则
	var rules []customRule
	if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
		http.Error(w, `{"error":"invalid json"}`, 400)
		return
	}
	// 逐条校验，拒绝脏数据（否则坏规则会静默落盘且永远匹配不上）
	for i, rule := range rules {
		rule.Value = strings.TrimSpace(rule.Value)
		switch rule.Type {
		case "domain", "keyword":
			if rule.Value == "" {
				http.Error(w, fmt.Sprintf(`{"error":"第 %d 条规则值为空"}`, i+1), 400)
				return
			}
		case "ipcidr":
			if _, _, err := net.ParseCIDR(rule.Value); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"第 %d 条 ipcidr 格式无效: %s"}`, i+1, rule.Value), 400)
				return
			}
		default:
			http.Error(w, fmt.Sprintf(`{"error":"第 %d 条类型无效: %s"}`, i+1, rule.Type), 400)
			return
		}
		if rule.Action != "proxy" && rule.Action != "direct" {
			http.Error(w, fmt.Sprintf(`{"error":"第 %d 条动作无效: %s（仅支持 proxy/direct）"}`, i+1, rule.Action), 400)
			return
		}
		rules[i].Value = rule.Value
	}

	customRulesMu.Lock()
	panelRules = rules
	rebuildCustomRulesLocked()
	total := len(customRules)
	customRulesMu.Unlock()

	// 持久化到文件
	if err := saveCustomRules(rulesData); err != nil {
		log.Printf("[规则] 保存规则失败: %v", err)
	} else {
		log.Printf("[规则] 已保存 %d 条面板规则到 %s（生效合计 %d 条）", len(rules), rulesData, total)
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleConnections 获取活跃连接（支持搜索和排序）
func handleConnections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	conns := getActiveConns()

	// 搜索过滤
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	if search != "" {
		var filtered []connInfoResp
		for _, c := range conns {
			if strings.Contains(strings.ToLower(c.ID), search) ||
				strings.Contains(strings.ToLower(c.Source), search) ||
				strings.Contains(strings.ToLower(c.Target), search) ||
				strings.Contains(strings.ToLower(c.Mode), search) ||
				strings.Contains(strings.ToLower(c.Rule), search) ||
				strings.Contains(strings.ToLower(c.Matched), search) {
				filtered = append(filtered, c)
			}
		}
		conns = filtered
	}

	// 排序
	sortBy := r.URL.Query().Get("sort")
	sortOrder := r.URL.Query().Get("order") // "asc" or "desc", default asc
	if sortBy != "" {
		desc := sortOrder == "desc"
		sort.Slice(conns, func(i, j int) bool {
			var less bool
			switch sortBy {
			case "id":
				less = conns[i].ID < conns[j].ID
			case "source":
				less = conns[i].Source < conns[j].Source
			case "target":
				less = conns[i].Target < conns[j].Target
			case "mode":
				less = conns[i].Mode < conns[j].Mode
			case "rule":
				less = conns[i].Rule < conns[j].Rule
			case "upload":
				less = conns[i].Upload < conns[j].Upload
			case "download":
				less = conns[i].Download < conns[j].Download
			case "start_time":
				less = conns[i].StartTime.Before(conns[j].StartTime)
			default:
				less = conns[i].StartTime.Before(conns[j].StartTime)
			}
			if desc {
				return !less
			}
			return less
		})
	}

	json.NewEncoder(w).Encode(conns)
}

// handleTraffic 获取流量统计
func handleTraffic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total_upload":   totalUpload.Load(),
		"total_download": totalDownload.Load(),
		"total_conns":    totalConns.Load(),
		"uptime":         time.Since(startTime).Seconds(),
	})
}

// handleLogs 获取日志
func handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	logBufferMu.Lock()
	logs := make([]logEntry, len(logBuffer))
	copy(logs, logBuffer)
	logBufferMu.Unlock()

	// 返回最新的日志 (倒序)
	sort.Slice(logs, func(i, j int) bool {
		return i > j
	})

	limit := 100
	if len(logs) < limit {
		limit = len(logs)
	}
	json.NewEncoder(w).Encode(logs[:limit])
}

// handleService 服务控制
func handleService(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	action := strings.TrimPrefix(r.URL.Path, "/api/service/")

	switch action {
	case "refresh-ech":
		if err := refreshECH(); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "ECH 配置已刷新"})
	case "reload-rules":
		if rulesFile == "" {
			json.NewEncoder(w).Encode(map[string]string{"error": "未配置规则文件"})
			return
		}
		if err := loadCustomRules(rulesFile); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "规则已重载"})
	default:
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown action"})
	}
}

// ======================== WebSocket 实时推送 ========================

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Web] WebSocket 升级失败: %v", err)
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// 用于计算实时速度（单位 B/s）
	var prevUp, prevDn int64
	var prevTime time.Time
	firstTick := true

	for {
		select {
		case <-ticker.C:
			// 计算实时流量（活跃连接的流量之和）
			var rtUp, rtDn int64
			activeConns.Range(func(key, value interface{}) bool {
				info := value.(*connInfo)
				rtUp += info.upload.Load()
				rtDn += info.download.Load()
				return true
			})

			// 计算增量速度（总上传/下载 - 上次值）/ 实际间隔秒数
			curUp := totalUpload.Load() + rtUp
			curDn := totalDownload.Load() + rtDn
			now := time.Now()
			speedUp := int64(0)
			speedDn := int64(0)
			if !firstTick {
				dt := now.Sub(prevTime).Seconds()
				if dt > 0 {
					speedUp = int64(float64(curUp-prevUp) / dt)
					speedDn = int64(float64(curDn-prevDn) / dt)
					if speedUp < 0 {
						speedUp = 0
					}
					if speedDn < 0 {
						speedDn = 0
					}
				}
			}
			firstTick = false
			prevUp = curUp
			prevDn = curDn
			prevTime = now

			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			data := map[string]interface{}{
				"active_conns":   activeConnCnt.Load(),
				"total_upload":   curUp,
				"total_download": curDn,
				"rt_upload":      speedUp,
				"rt_download":    speedDn,
				"uptime":         time.Since(startTime).Seconds(),
				"memory_mb":      float64(m.Alloc) / 1024 / 1024,
			}
			if err := conn.WriteJSON(data); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// 出口 IP / Cloudflare COLO 检测：启动 5 秒后探测一次供首屏展示，
// 之后只在查看概览页（/api/exit-info）缓存过期或 ?refresh=1 时按需检测，
// 平时不产生探测连接。

var (
	cachedExitIP  string
	cachedCOLO    string
	exitInfoAt    time.Time
	exitInfoMu    sync.RWMutex
	exitDetecting atomic.Bool
)

// exitInfoTTL 出口信息缓存有效期：只在概览页按需检测，平时不产生探测连接
const exitInfoTTL = 10 * time.Minute

// startExitInfoDetector 仅启动后探测一次供首屏展示；之后只在查看概览页（/api/exit-info）时按需检测
func startExitInfoDetector() {
	go func() {
		time.Sleep(5 * time.Second)
		detectExitInfo()
	}()
}

// triggerExitDetect 触发一次后台检测（防并发：检测中直接返回）
func triggerExitDetect() {
	if exitDetecting.CompareAndSwap(false, true) {
		go func() {
			defer exitDetecting.Store(false)
			detectExitInfo()
		}()
	}
}

// detectExitInfo 通过本地代理检测出口 IP 与 Cloudflare COLO
func detectExitInfo() {
	log.Printf("[检测] === 开始检测出口IP ===")

	// 通过本地 HTTP 代理检测出口 IP（避免 Worker 不支持 80 端口的问题）
	localProxy := "http://127.0.0.1" + listenAddr[strings.Index(listenAddr, ":"):]
	proxyURL, _ := url.Parse(localProxy)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	// 检测出口 IP（多源轮询，任一成功即可）
	exitIP := ""
	ipSources := []string{
		"https://api.ipify.org?format=json",
		"https://ifconfig.me/ip",
		"https://api.ip.sb/ip",
	}
	for _, src := range ipSources {
		if ip := fetchExitIP(client, src); ip != "" {
			exitIP = ip
			break
		}
	}
	if exitIP == "" {
		log.Printf("[检测] 获取出口IP失败（已试 %d 个源）", len(ipSources))
	}

	// 检测 COLO（多源 Cloudflare trace，任一成功即可）
	colo := ""
	coloSources := []string{
		"https://1.1.1.1/cdn-cgi/trace",
		"https://www.cloudflare.com/cdn-cgi/trace",
		"https://cloudflare.com/cdn-cgi/trace",
	}
	for _, src := range coloSources {
		if c := fetchCOLO(client, src); c != "" {
			colo = c
			break
		}
	}

	// 只在成功时更新缓存：失败不擦除上次的结果
	exitInfoMu.Lock()
	if exitIP != "" {
		cachedExitIP = exitIP
	}
	if colo != "" {
		cachedCOLO = colo
	}
	if exitIP != "" || colo != "" {
		exitInfoAt = time.Now()
	}
	exitInfoMu.Unlock()

	if exitIP != "" {
		log.Printf("[检测] 出口IP: %s, COLO: %s", exitIP, colo)
	}
}

// fetchExitIP 从单个源获取出口 IP（JSON 或纯文本），失败返回 ""
func fetchExitIP(client *http.Client, src string) string {
	resp, err := client.Get(src)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	body, _ := io.ReadAll(resp.Body)
	// 先按 JSON 解析（ipify），失败则按纯文本处理，结果必须是合法 IP
	var ipResp struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(body, &ipResp) == nil && net.ParseIP(strings.TrimSpace(ipResp.IP)) != nil {
		return strings.TrimSpace(ipResp.IP)
	}
	if ip := net.ParseIP(strings.TrimSpace(string(body))); ip != nil {
		return ip.String()
	}
	return ""
}

// fetchCOLO 从单个 Cloudflare trace 源解析 colo，失败返回 ""
func fetchCOLO(client *http.Client, src string) string {
	resp, err := client.Get(src)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	body, _ := io.ReadAll(resp.Body)
	traceStr := string(body)
	if idx := strings.Index(traceStr, "colo="); idx >= 0 {
		start := idx + 5
		end := start
		for end < len(traceStr) && traceStr[end] != '\r' && traceStr[end] != '\n' {
			end++
		}
		return traceStr[start:end]
	}
	return ""
}

// handleExitInfo 返回缓存的出口信息；缓存过期或 ?refresh=1 时触发一次后台检测
func handleExitInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Query().Get("refresh") == "1" {
		triggerExitDetect()
	}
	exitInfoMu.RLock()
	ip, colo, at := cachedExitIP, cachedCOLO, exitInfoAt
	exitInfoMu.RUnlock()
	stale := at.IsZero() || time.Since(at) > exitInfoTTL
	if stale {
		triggerExitDetect()
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"exit_ip":    ip,
		"colo":       colo,
		"updated_at": at.Unix(),
		"stale":      stale,
	})
}

// 每日定时自重启：到达 -restart-at 时间后进程内 re-exec（Unix）或退化为退出进程，
// 用于清理长时间运行后的状态劣化，不依赖容器 restart 策略。

// startScheduledRestart 每日定时退出进程（容器无 restart 策略也能自愈），
// 用于清理长时间运行后的状态劣化（如吞吐劣化、连接悬挂）。时间取容器本地时区（TZ）。
func startScheduledRestart() {
	raw := strings.TrimSpace(restartAtRaw)
	if raw == "" || strings.EqualFold(raw, "off") || raw == "0" || raw == "disabled" {
		log.Printf("[定时重启] 已关闭（-restart-at / ECH_RESTART_AT = %q）", raw)
		return
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		log.Printf("[警告] 定时重启时间格式无效 (%q)，已关闭（格式: HH:MM，off=关闭）", raw)
		return
	}
	h, errH := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, errM := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		log.Printf("[警告] 定时重启时间无效 (%q)，已关闭（格式: HH:MM，off=关闭）", raw)
		return
	}

	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	log.Printf("[定时重启] 已启用: 每日 %02d:%02d 自动重启，下次 %s", h, m, next.Format("2006-01-02 15:04:05"))

	if d := time.Until(next); d > 0 {
		time.Sleep(d)
	}

	log.Printf("[定时重启] 到达 %02d:%02d，正在自我重启刷新进程（不中断容器）...", h, m)
	// 留出日志落盘/面板可见的时间
	time.Sleep(500 * time.Millisecond)

	// 进程内 re-exec：同 PID 原地替换镜像，旧状态清零，不依赖容器 restart 策略
	if exe, err := os.Executable(); err == nil {
		if err := selfRestart(exe); err != nil {
			log.Printf("[警告] 定时重启: 自我重启失败（%v），退化为退出进程", err)
		}
	} else {
		log.Printf("[警告] 定时重启: 获取可执行路径失败（%v），退化为退出进程", err)
	}
	// 仅 re-exec 失败时走到这里：退出进程（若容器有 restart 策略则由其拉起）
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
