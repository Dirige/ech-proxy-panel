package main

// 出口 IP / Cloudflare COLO 检测：启动 5 秒后探测一次供首屏展示，
// 之后只在查看概览页（/api/exit-info）缓存过期或 ?refresh=1 时按需检测，
// 平时不产生探测连接。

import (
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

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
