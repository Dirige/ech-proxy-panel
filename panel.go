package main

// ======================== Web 管理面板（叠加层） ========================
//
// 本文件与 panel_rules.go / panel_stats.go / panel_web.go / panel_restart.go /
// panel_exit.go 为「在原版 ech-workers.go 之上叠加」的面板实现。
// 原版数据面保持逐字节不动，仅通过 ech-workers.go 内 4 处 [面板钩子 A~D] 接入：
//	A = main 中 flag.Parse() 后调用 panelStartup()（本文件）
//	B = main 中分流模式分支补 custom（避免被原版重置为 global）
//	C = handleTunnel 分流决策入口（规则/统计，见 panel_stats.go）
//	D = 拨通 WS 后记录实拨地址（见 panel_stats.go）
//
// 参数优先级（原版 flag 语义为王）：
//	命令行(flag.Visit 显式传入) > 环境变量 ECH_* > 配置文件 > 原版 flag 默认值

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

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
