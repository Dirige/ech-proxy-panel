package main

// Web 管理面板：HTTP 服务、登录鉴权、四页（概览/连接/流量/规则配置）API 与 WebSocket 实时推送。

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

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
