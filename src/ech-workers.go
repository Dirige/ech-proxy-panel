package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed index.html
var indexHTML embed.FS

// ======================== 鍏ㄥ眬鍙傛暟 ========================

var (
	listenAddr  string
	serverAddr  string
	serverIP    string
	token       string
	dnsServer   string
	echDomain   string
	routingMode string // 鍒嗘祦妯″紡: "global", "bypass_cn", "none", "custom"
	webAddr     string // Web 绠＄悊闈㈡澘鐩戝惉鍦板潃
	rulesFile   string // 鑷畾涔夎鍒欐枃浠惰矾寰?	rulesData   string // 闈㈡澘瑙勫垯鎸佷箙鍖栨枃浠惰矾寰?	configFile  string // 閰嶇疆鏂囦欢璺緞
	proxyIP     string // 鍥哄畾鍑哄彛 IP锛堟牸寮? ip 鎴?ip:port锛?	webPassword string // Web 绠＄悊闈㈡澘鐧诲綍瀵嗙爜锛堜负绌哄垯涓嶉渶鐧诲綍锛?
	// ========== 闀胯繛鎺ワ紙瑙嗛锛夌ǔ瀹氭€х浉鍏?==========
	downLimitMbps float64       // 鍗曡繛鎺ヤ笅琛岄檺閫燂紙Mbps锛夛紝0 = 涓嶉檺閫?	idleTimeout   time.Duration // 闅ч亾绌洪棽瓒呮椂锛堟棤浠讳綍鏁版嵁浜や簰澶氫箙鍚庝富鍔ㄥ叧闂級锛? = 涓嶄富鍔ㄥ叧闂?
	// Web 浼氳瘽绠＄悊
	webSessions   map[string]time.Time // sessionID -> 杩囨湡鏃堕棿
	webSessionsMu sync.Mutex

	echListMu sync.RWMutex
	echList   []byte

	// 涓浗IP鍒楄〃锛圛Pv4锛?	chinaIPRangesMu sync.RWMutex
	chinaIPRanges   []ipRange

	// 涓浗IP鍒楄〃锛圛Pv6锛?	chinaIPV6RangesMu sync.RWMutex
	chinaIPV6Ranges   []ipRangeV6

	// ========== Web 绠＄悊闈㈡澘鐩稿叧 ==========

	// 杩炴帴杩借釜
	activeConns   sync.Map // key: connID(string) -> *connInfo
	connIDCounter atomic.Int64
	activeConnCnt atomic.Int64 // 娲昏穬杩炴帴鏁帮紙閬垮厤鍏ㄩ噺閬嶅巻锛?
	// 娴侀噺缁熻
	totalUpload   atomic.Int64
	totalDownload atomic.Int64
	totalConns    atomic.Int64
	startTime     time.Time

	// 鏃ュ織缂撳啿 (鏈€杩?200 鏉?
	logBuffer   []logEntry
	logBufferMu sync.Mutex

	// 鑷畾涔夎鍒?	customRules   []customRule
	customRulesMu sync.RWMutex

	// DNS 瑙ｆ瀽缁撴灉缂撳瓨锛坆ypass_cn 妯″紡涓嬮伩鍏嶉噸澶嶆煡璇級
	dnsCache   map[string]dnsCacheEntry
	dnsCacheMu sync.RWMutex
)

type dnsCacheEntry struct {
	ips       []net.IP
	expiresAt time.Time
}

// ipRange 琛ㄧず涓€涓狪Pv4 IP鑼冨洿
type ipRange struct {
	start uint32
	end   uint32
}

// ipRangeV6 琛ㄧず涓€涓狪Pv6 IP鑼冨洿
type ipRangeV6 struct {
	start [16]byte
	end   [16]byte
}

// ========== Web 绠＄悊闈㈡澘绫诲瀷 ==========

// connInfo 杩炴帴淇℃伅
type connInfo struct {
	ID        string
	Source    string
	Target    string
	Mode      string
	Rule      string
	StartTime time.Time
	upload    atomic.Int64
	download  atomic.Int64
}

// connInfoResp 鐢ㄤ簬 JSON 搴忓垪鍖栫殑杩炴帴淇℃伅锛堜笉鍚?atomic 瀛楁锛?type connInfoResp struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Target    string    `json:"target"`
	Mode      string    `json:"mode"`
	Rule      string    `json:"rule"`
	Upload    int64     `json:"upload"`
	Download  int64     `json:"download"`
	StartTime time.Time `json:"start_time"`
}

// logEntry 鏃ュ織鏉＄洰
type logEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// customRule 鑷畾涔夎鍒?type customRule struct {
	Type   string `json:"type"`   // domain, ipcidr, keyword
	Value  string `json:"value"`  // 瑙勫垯鍊?	Action string `json:"action"` // proxy, direct
}

// appConfig 搴旂敤閰嶇疆
type appConfig struct {
	ListenAddr  string `json:"listen_addr"`
	ServerAddr  string `json:"server_addr"`
	ServerIP    string `json:"server_ip"`
	Token       string `json:"token"`
	DNSServer   string `json:"dns_server"`
	ECHDomain   string `json:"ech_domain"`
	RoutingMode string `json:"routing_mode"`
	WebAddr     string `json:"web_addr"`
	ProxyIP     string `json:"proxy_ip"`
	WebPassword string `json:"web_password"`
	// 闀胯繛鎺ワ紙瑙嗛锛夌ǔ瀹氭€?	DownLimitMbps float64 `json:"down_limit_mbps"` // 鍗曡繛鎺ヤ笅琛岄檺閫燂紙Mbps锛夛紝0 = 涓嶉檺閫?	IdleTimeoutMin float64 `json:"idle_timeout_min"` // 闅ч亾绌洪棽瓒呮椂锛堝垎閽燂級锛? = 涓嶄富鍔ㄥ叧闂?}

// loadConfig 浠庢枃浠跺姞杞介厤缃?func loadConfig(filePath string) (*appConfig, error) {
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

// saveConfig 淇濆瓨閰嶇疆鍒版枃浠?func saveConfig(filePath string) error {
	cfg := appConfig{
		ListenAddr:  listenAddr,
		ServerAddr:  serverAddr,
		ServerIP:    serverIP,
		Token:       token,
		DNSServer:   dnsServer,
		ECHDomain:   echDomain,
		RoutingMode: routingMode,
		WebAddr:     webAddr,
		ProxyIP:     proxyIP,
		WebPassword: webPassword,
		DownLimitMbps: downLimitMbps,
		IdleTimeoutMin: idleTimeout.Minutes(),
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(filePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("鍒涘缓閰嶇疆鐩綍澶辫触: %w", err)
		}
	}
	return os.WriteFile(filePath, data, 0600)
}

// statusResponse 鐘舵€佸搷搴?type statusResponse struct {
	Uptime       string  `json:"uptime"`
	TotalConns   int64   `json:"total_conns"`
	ActiveConns  int     `json:"active_conns"`
	TotalUpload  int64   `json:"total_upload"`
	TotalDownload int64  `json:"total_download"`
	RoutingMode  string  `json:"routing_mode"`
	ListenAddr   string  `json:"listen_addr"`
	ServerAddr   string  `json:"server_addr"`
	ECHDomain    string  `json:"ech_domain"`
	WebAddr      string  `json:"web_addr"`
	CustomRules  int     `json:"custom_rules"`
	MemoryMB     float64 `json:"memory_mb"`
}

func init() {
	flag.StringVar(&listenAddr, "l", "0.0.0.0:30000", "浠ｇ悊鐩戝惉鍦板潃 (鏀寔 SOCKS5 鍜?HTTP)")
	flag.StringVar(&serverAddr, "f", "hhech.nb1tap.kdns.fr:443", "鏈嶅姟绔湴鍧€ (鏍煎紡: x.x.workers.dev:443)")
	flag.StringVar(&serverIP, "ip", "", "鎸囧畾鏈嶅姟绔?IP锛堢粫杩?DNS 瑙ｆ瀽锛?)
	flag.StringVar(&token, "token", "honghongfree", "韬唤楠岃瘉浠ょ墝")
	flag.StringVar(&dnsServer, "dns", "dns.alidns.com/dns-query", "ECH 鏌ヨ DoH 鏈嶅姟鍣?)
	flag.StringVar(&echDomain, "ech", "cloudflare-ech.com", "ECH 鏌ヨ鍩熷悕")
	flag.StringVar(&routingMode, "routing", "bypass_cn", "鍒嗘祦妯″紡: global(鍏ㄥ眬浠ｇ悊), bypass_cn(璺宠繃涓浗澶ч檰), none(涓嶆敼鍙樹唬鐞?, custom(鑷畾涔夎鍒?")
	flag.StringVar(&webAddr, "web", "", "Web 绠＄悊闈㈡澘鐩戝惉鍦板潃 (濡?:9090)")
	flag.StringVar(&rulesFile, "rules", "", "鑷畾涔夎鍒欐枃浠惰矾寰?(routing=custom 鏃跺繀闇€)")
	flag.StringVar(&rulesData, "rules-data", "/data/rules.json", "闈㈡澘瑙勫垯鎸佷箙鍖栨枃浠惰矾寰?)
	flag.StringVar(&configFile, "config", "/data/config.json", "閰嶇疆鏂囦欢璺緞")
	flag.StringVar(&proxyIP, "proxyip", "", "鍥哄畾鍑哄彛 IP (濡?101.79.165.113 鎴?101.79.165.113:443)")
	flag.StringVar(&webPassword, "password", "", "Web 绠＄悊闈㈡澘鐧诲綍瀵嗙爜 (涓虹┖鍒欎笉闇€鐧诲綍)")
	flag.Float64Var(&downLimitMbps, "downlimit", 0, "鍗曡繛鎺ヤ笅琛岄檺閫?Mbps (0=涓嶉檺)銆傜湅瑙嗛鍗￠】鏃跺缓璁涓虹爜鐜囩殑 1.2~1.5 鍊?)
	flag.DurationVar(&idleTimeout, "idle-timeout", 15*time.Minute, "闅ч亾绌洪棽瓒呮椂锛屾棤浠讳綍鏁版嵁浜や簰瓒呰繃璇ユ椂闀垮垯鍏抽棴 (0=姘镐笉涓诲姩鍏抽棴)")
}

// applyEnvDefaults 浠庣幆澧冨彉閲忓姞杞介粯璁ゅ€硷紙鍛戒护琛屽弬鏁颁紭鍏堬級
// 鏀寔: ECH_LISTEN, ECH_SERVER, ECH_SERVER_IP, ECH_TOKEN, ECH_DNS, ECH_DOMAIN,
//        ECH_ROUTING, ECH_WEB, ECH_PROXY_IP, ECH_PASSWORD, ECH_CONFIG, ECH_RULES_DATA
func applyEnvDefaults() {
	if v := os.Getenv("ECH_LISTEN"); v != "" && listenAddr == "0.0.0.0:30000" {
		listenAddr = v
	}
	if v := os.Getenv("ECH_SERVER"); v != "" && serverAddr == "hhech.nb1tap.kdns.fr:443" {
		serverAddr = v
	}
	if v := os.Getenv("ECH_SERVER_IP"); v != "" && serverIP == "" {
		serverIP = v
	}
	if v := os.Getenv("ECH_TOKEN"); v != "" && token == "honghongfree" {
		token = v
	}
	if v := os.Getenv("ECH_DNS"); v != "" && dnsServer == "dns.alidns.com/dns-query" {
		dnsServer = v
	}
	if v := os.Getenv("ECH_DOMAIN"); v != "" && echDomain == "cloudflare-ech.com" {
		echDomain = v
	}
	if v := os.Getenv("ECH_ROUTING"); v != "" && routingMode == "bypass_cn" {
		routingMode = v
	}
	if v := os.Getenv("ECH_WEB"); v != "" && webAddr == "" {
		webAddr = v
	}
	if v := os.Getenv("ECH_PROXY_IP"); v != "" && proxyIP == "" {
		proxyIP = v
	}
	if v := os.Getenv("ECH_PASSWORD"); v != "" && webPassword == "" {
		webPassword = v
	}
	if v := os.Getenv("ECH_CONFIG"); v != "" && configFile == "/data/config.json" {
		configFile = v
	}
	if v := os.Getenv("ECH_RULES_DATA"); v != "" && rulesData == "/data/rules.json" {
		rulesData = v
	}
	if v := os.Getenv("ECH_DOWN_LIMIT"); v != "" && downLimitMbps == 0 {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			downLimitMbps = f
		}
	}
	if v := os.Getenv("ECH_IDLE_TIMEOUT"); v != "" && idleTimeout == 15*time.Minute {
		if d, err := time.ParseDuration(v); err == nil {
			idleTimeout = d
		}
	}
}

func main() {
	flag.Parse()

	// 鐜鍙橀噺锛堝懡浠よ鍙傛暟浼樺厛锛岀幆澧冨彉閲忓叾娆★紝config.json 鏈€鍚庯級
	applyEnvDefaults()

	// 鍔犺浇閰嶇疆鏂囦欢锛堝懡浠よ鍙傛暟 > 鐜鍙橀噺 > config.json锛?	if cfg, err := loadConfig(configFile); err == nil {
		log.Printf("[鍚姩] 宸插姞杞介厤缃枃浠? %s", configFile)
		if listenAddr == "0.0.0.0:30000" && cfg.ListenAddr != "" {
			listenAddr = cfg.ListenAddr
		}
		if serverAddr == "hhech.nb1tap.kdns.fr:443" && cfg.ServerAddr != "" {
			serverAddr = cfg.ServerAddr
		}
		if serverIP == "" && cfg.ServerIP != "" {
			serverIP = cfg.ServerIP
		}
		if token == "honghongfree" && cfg.Token != "" {
			token = cfg.Token
		}
		if dnsServer == "dns.alidns.com/dns-query" && cfg.DNSServer != "" {
			dnsServer = cfg.DNSServer
		}
		if echDomain == "cloudflare-ech.com" && cfg.ECHDomain != "" {
			echDomain = cfg.ECHDomain
		}
		if routingMode == "bypass_cn" && cfg.RoutingMode != "" {
			routingMode = cfg.RoutingMode
		}
		if webAddr == "" && cfg.WebAddr != "" {
			webAddr = cfg.WebAddr
		}
		if proxyIP == "" && cfg.ProxyIP != "" {
			proxyIP = cfg.ProxyIP
		}
		if webPassword == "" && cfg.WebPassword != "" {
			webPassword = cfg.WebPassword
		}
		if downLimitMbps == 0 && cfg.DownLimitMbps > 0 {
			downLimitMbps = cfg.DownLimitMbps
		}
		if idleTimeout == 15*time.Minute && cfg.IdleTimeoutMin > 0 {
			idleTimeout = time.Duration(cfg.IdleTimeoutMin * float64(time.Minute))
		}
	} else if configFile != "" {
		log.Printf("[鍚姩] 閰嶇疆鏂囦欢涓嶅瓨鍦ㄦ垨鏃犳晥锛屼娇鐢ㄥ懡浠よ鍙傛暟")
	}

	// 濡傛灉娌℃湁閰嶇疆鏂囦欢涓斿懡浠よ鏈夊弬鏁帮紝鑷姩淇濆瓨閰嶇疆
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		if serverAddr != "" {
			if err := saveConfig(configFile); err != nil {
				log.Printf("[鍚姩] 淇濆瓨閰嶇疆鏂囦欢澶辫触: %v", err)
			} else {
				log.Printf("[鍚姩] 宸蹭繚瀛橀厤缃埌: %s", configFile)
			}
		}
	}

	if serverAddr == "" {
		log.Fatal("蹇呴』鎸囧畾鏈嶅姟绔湴鍧€ -f\n\n绀轰緥:\n  ./client -l 127.0.0.1:1080 -f your-worker.workers.dev:443 -token your-token")
	}

	log.Printf("[鍚姩] 姝ｅ湪鑾峰彇 ECH 閰嶇疆...")
	if err := prepareECH(); err != nil {
		log.Fatalf("[鍚姩] 鑾峰彇 ECH 閰嶇疆澶辫触: %v", err)
	}

	// 鍒濆鍖栫粺璁?	startTime = time.Now()
	logBuffer = make([]logEntry, 0, 200)
	dnsCache = make(map[string]dnsCacheEntry)

	// 璁剧疆鏃ュ織鍚屾椂鍐欏叆缂撳啿鍖?	log.SetOutput(&logWriter{original: os.Stderr})
	log.Printf("[娴嬭瘯] 鏃ュ織绯荤粺宸插垵濮嬪寲")

	// 鍔犺浇鑷畾涔夎鍒?	if routingMode == "custom" {
		if rulesFile == "" {
			log.Fatal("[鍚姩] custom 鍒嗘祦妯″紡闇€瑕佹寚瀹氳鍒欐枃浠?-rules")
		}
		if err := loadCustomRules(rulesFile); err != nil {
			log.Fatalf("[鍚姩] 鍔犺浇鑷畾涔夎鍒欏け璐? %v", err)
		}
		customRulesMu.RLock()
		log.Printf("[鍚姩] 宸插姞杞?%d 鏉¤嚜瀹氫箟瑙勫垯", len(customRules))
		customRulesMu.RUnlock()
	}

	// 鍔犺浇涓浗IP鍒楄〃锛堝缁堝姞杞斤紝渚涘垎娴佸拰鍚庡彴鏇存柊浣跨敤锛?	{
		ipv4Count := 0
		ipv6Count := 0

		if err := loadChinaIPList(); err != nil {
			log.Printf("[璀﹀憡] 鍔犺浇涓浗IPv4鍒楄〃澶辫触: %v", err)
		} else {
			chinaIPRangesMu.RLock()
			ipv4Count = len(chinaIPRanges)
			chinaIPRangesMu.RUnlock()
		}

		if err := loadChinaIPV6List(); err != nil {
			log.Printf("[璀﹀憡] 鍔犺浇涓浗IPv6鍒楄〃澶辫触: %v", err)
		} else {
			chinaIPV6RangesMu.RLock()
			ipv6Count = len(chinaIPV6Ranges)
			chinaIPV6RangesMu.RUnlock()
		}

		if ipv4Count > 0 || ipv6Count > 0 {
			log.Printf("[鍚姩] 宸插姞杞?%d 涓腑鍥絀Pv4娈? %d 涓腑鍥絀Pv6娈?, ipv4Count, ipv6Count)
		} else {
			log.Printf("[璀﹀憡] 鏈姞杞藉埌浠讳綍涓浗IP鍒楄〃")
		}
	}

	// ProxyIP 鎻愮ず
	if proxyIP != "" {
		log.Printf("[鍚姩] 鍥哄畾鍑哄彛 IP: %s", proxyIP)
	}

	// 鍚姩鍑哄彛IP妫€娴?	log.Printf("[鍚姩] 鍚姩鍑哄彛IP妫€娴嬪櫒...")

	// 鍔犺浇闈㈡澘瑙勫垯鎸佷箙鍖栨枃浠?	loadRulesDataFile()

	startExitInfoDetector()

	// 鍒濆鍖?web 浼氳瘽绠＄悊
	webSessions = make(map[string]time.Time)
	go startSessionGC()

	// 鍚姩 Web 绠＄悊闈㈡澘
	if webAddr != "" {
		go startWebServer(webAddr)
	}

	// 鍚姩鍚庡彴 IP 鍒楄〃鏇存柊鍣紙浠ｇ悊灏辩华鍚庣粡闅ч亾鏇存柊锛?	go startChinaIPBackgroundUpdater()

	// 浼橀泤閫€鍑?	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("[閫€鍑篯 鏀跺埌缁堟淇″彿锛屾鍦ㄥ叧闂?..")
		os.Exit(0)
	}()

	runProxyServer(listenAddr)
}

// ======================== 鏃ュ織缂撳啿 ========================

type logWriter struct {
	original io.Writer
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			w.original.Write([]byte(fmt.Sprintf("[logWriter panic: %v]\n", r)))
		}
	}()
	msg := strings.TrimSpace(string(p))
	if msg != "" {
		level := "info"
		if strings.Contains(msg, "[璀﹀憡]") || strings.Contains(msg, "warn") {
			level = "warn"
		} else if strings.Contains(msg, "[閿欒]") || strings.Contains(msg, "fatal") || strings.Contains(msg, "error") {
			level = "error"
		}
		addLog(level, msg)
	}
	return w.original.Write(p)
}

// ======================== 宸ュ叿鍑芥暟 ========================

// ipToUint32 灏咺P鍦板潃杞崲涓簎int32
func ipToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// isChinaIP 妫€鏌P鏄惁鍦ㄤ腑鍥絀P鍒楄〃涓紙鏀寔IPv4鍜孖Pv6锛?func isChinaIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// 妫€鏌Pv4
	if ip.To4() != nil {
		ipUint32 := ipToUint32(ip)
		if ipUint32 == 0 {
			return false
		}

		chinaIPRangesMu.RLock()
		defer chinaIPRangesMu.RUnlock()

		return findIPv4Range(chinaIPRanges, ipUint32)
	}

	// 妫€鏌Pv6
	ipBytes := ip.To16()
	if ipBytes == nil {
		return false
	}

	var ipArray [16]byte
	copy(ipArray[:], ipBytes)

	chinaIPV6RangesMu.RLock()
	defer chinaIPV6RangesMu.RUnlock()

	// 浜屽垎鏌ユ壘IPv6
	left, right := 0, len(chinaIPV6Ranges)
	for left < right {
		mid := (left + right) / 2
		r := chinaIPV6Ranges[mid]

		// 姣旇緝璧峰IP
		cmpStart := compareIPv6(ipArray, r.start)
		if cmpStart < 0 {
			right = mid
			continue
		}

		// 姣旇緝缁撴潫IP
		cmpEnd := compareIPv6(ipArray, r.end)
		if cmpEnd > 0 {
			left = mid + 1
			continue
		}

		// 鍦ㄨ寖鍥村唴
		return true
	}
	return false
}

// compareIPv6 姣旇緝涓や釜IPv6鍦板潃锛岃繑鍥?-1, 0, 鎴?1
func compareIPv6(a, b [16]byte) int {
	for i := 0; i < 16; i++ {
		if a[i] < b[i] {
			return -1
		} else if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// downloadIPList 涓嬭浇IP鍒楄〃鏂囦欢
func downloadIPList(url, filePath string) error {
	log.Printf("[涓嬭浇] 姝ｅ湪涓嬭浇 IP 鍒楄〃: %s", url)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("涓嬭浇澶辫触: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("涓嬭浇澶辫触: HTTP %d", resp.StatusCode)
	}

	// 璇诲彇鍐呭
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("璇诲彇涓嬭浇鍐呭澶辫触: %w", err)
	}

	// 淇濆瓨鍒版枃浠?	if err := os.WriteFile(filePath, content, 0644); err != nil {
		return fmt.Errorf("淇濆瓨鏂囦欢澶辫触: %w", err)
	}

	log.Printf("[涓嬭浇] 宸蹭繚瀛樺埌: %s", filePath)
	return nil
}

// startChinaIPBackgroundUpdater 浠ｇ悊灏辩华鍚庣粡 ECH 闅ч亾鍚庡彴鏇存柊涓浗 IP 鍒楄〃
func startChinaIPBackgroundUpdater() {
	// 绛夊緟浠ｇ悊鍚姩瀹屾垚
	time.Sleep(10 * time.Second)

	interval := 24 * time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		// 妫€鏌ヤ唬鐞嗘槸鍚﹀彲鐢?		if activeConnCnt.Load() == 0 && !isProxyReady() {
			log.Printf("[鏇存柊] 浠ｇ悊灏氭湭灏辩华锛岃烦杩囨湰娆℃洿鏂?)
			continue
		}

		log.Printf("[鏇存柊] 寮€濮嬪悗鍙版洿鏂颁腑鍥絀P鍒楄〃...")

		// 涓嬭浇 IPv4 鍒楄〃
		if err := downloadAndApplyIPList(
			"https://raw.githubusercontent.com/mayaxcn/china-ip-list/refs/heads/master/chnroute.txt",
			"/data/chn_ip.txt",
			loadChinaIPList,
		); err != nil {
			log.Printf("[鏇存柊] IPv4 鍒楄〃鏇存柊澶辫触: %v", err)
		}

		// 涓嬭浇 IPv6 鍒楄〃
		if err := downloadAndApplyIPList(
			"https://raw.githubusercontent.com/mayaxcn/china-ip-list/refs/heads/master/chnroute_v6.txt",
			"/data/chn_ip_v6.txt",
			loadChinaIPV6List,
		); err != nil {
			log.Printf("[鏇存柊] IPv6 鍒楄〃鏇存柊澶辫触: %v", err)
		}
	}
}

// downloadAndApplyIPList 涓嬭浇銆佹牎楠屻€佸師瀛愭浛鎹?IP 鍒楄〃
func downloadAndApplyIPList(url, filePath string, loadFn func() error) error {
	// 涓嬭浇鍒颁复鏃舵枃浠?	tmpFile := filePath + ".tmp"
	if err := downloadIPList(url, tmpFile); err != nil {
		return err
	}

	// 灏濊瘯鍔犺浇鏂版枃浠惰繘琛屾牎楠?	if err := loadFn(); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("鏍￠獙澶辫触: %w", err)
	}

	// 鏍￠獙閫氳繃锛屾浛鎹㈠師鏂囦欢
	if err := os.Rename(tmpFile, filePath); err != nil {
		return fmt.Errorf("鏇挎崲鏂囦欢澶辫触: %w", err)
	}

	log.Printf("[鏇存柊] 宸叉洿鏂? %s", filePath)
	return nil
}

// isProxyReady 妫€鏌ヤ唬鐞嗘槸鍚﹀凡鍚姩锛堢畝鍗曟鏌ワ級
func isProxyReady() bool {
	conn, err := net.DialTimeout("tcp", listenAddr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// loadChinaIPList 鍔犺浇涓浗IPv4鍒楄〃锛堜紭鍏堣鍐呯疆鏂囦欢锛屽啀璇?/data 鎸佷箙鍖栨枃浠讹級
func loadChinaIPList() error {
	// 鏌ユ壘椤哄簭锛?data/鏇存柊鏂囦欢 > /usr/local/bin/鍐呯疆鏂囦欢 > ./褰撳墠鐩綍
	searchPaths := []string{
		"/data/chn_ip.txt",
		"/usr/local/bin/chn_ip.txt",
		"chn_ip.txt",
	}

	var ipListFile string
	for _, p := range searchPaths {
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			ipListFile = p
			break
		}
	}
	if ipListFile == "" {
		return fmt.Errorf("涓浗IPv4鍒楄〃鏂囦欢涓嶅瓨鍦紙宸插唴缃増鏈笉鍙敤锛?)
	}

	file, err := os.Open(ipListFile)
	if err != nil {
		return fmt.Errorf("鎵撳紑IP鍒楄〃鏂囦欢澶辫触: %w", err)
	}
	defer file.Close()

	var ranges []ipRange
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		// 鏀寔 CIDR 鏍煎紡: 1.0.1.0/24
		if len(parts) == 1 && strings.Contains(parts[0], "/") {
			_, cidr, err := net.ParseCIDR(parts[0])
			if err != nil {
				continue
			}
			start := ipToUint32(cidr.IP)
			end := ipToUint32(lastIP(cidr))
			if start > 0 && end > 0 && start <= end {
				ranges = append(ranges, ipRange{start: start, end: end})
			}
			continue
		}

		// 鏀寔 "startIP endIP" 鏍煎紡
		if len(parts) < 2 {
			continue
		}

		startIP := net.ParseIP(parts[0])
		endIP := net.ParseIP(parts[1])
		if startIP == nil || endIP == nil {
			continue
		}

		start := ipToUint32(startIP)
		end := ipToUint32(endIP)
		if start > 0 && end > 0 && start <= end {
			ranges = append(ranges, ipRange{start: start, end: end})
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("璇诲彇IP鍒楄〃鏂囦欢澶辫触: %w", err)
	}

	if len(ranges) == 0 {
		return errors.New("IP鍒楄〃涓虹┖")
	}

	// 鎸夎捣濮婭P鎺掑簭
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start
	})

	// 鍚堝苟閲嶅彔/鐩搁偦鍖洪棿锛屼繚璇佷簩鍒嗘煡鎵炬纭?	ranges = mergeIPv4Ranges(ranges)

	// 瀹夊叏妫€鏌ワ細纭繚宸茬煡鍥藉IP涓嶄細琚鍒や负涓浗IP
	foreignSamples := []string{"8.8.8.8", "1.1.1.1", "91.108.56.130", "172.67.74.152", "104.16.0.1"}
	bad := 0
	for _, s := range foreignSamples {
		if findIPv4Range(ranges, ipToUint32(net.ParseIP(s))) {
			bad++
		}
	}
	if bad > 0 {
		os.Remove(ipListFile) // 鍒犻櫎鎹熷潖鐨勫垪琛紝涓嬫鍚姩閲嶆柊涓嬭浇
		return fmt.Errorf("IP鍒楄〃鏍￠獙澶辫触: 宸茬煡鍥藉IP琚鍒や负涓浗IP (%d/%d)锛屽凡涓㈠純璇ュ垪琛ㄥ苟鍥為€€涓哄叏灞€浠ｇ悊", bad, len(foreignSamples))
	}

	chinaIPRangesMu.Lock()
	chinaIPRanges = ranges
	chinaIPRangesMu.Unlock()

	return nil
}

// lastIP 杩斿洖 CIDR 缃戠粶鐨勬渶鍚庝竴涓?IP
func lastIP(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	if ip == nil {
		return n.IP
	}
	last := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		last[i] = ip[i] | ^n.Mask[i]
	}
	return last
}

// lastIPv6 杩斿洖 IPv6 CIDR 缃戠粶鐨勬渶鍚庝竴涓?IP
func lastIPv6(n *net.IPNet) net.IP {
	ip := n.IP.To16()
	if ip == nil {
		return nil
	}
	last := make(net.IP, 16)
	for i := 0; i < 16; i++ {
		last[i] = ip[i] | ^n.Mask[i]
	}
	return last
}

// mergeIPv4Ranges 鍚堝苟閲嶅彔鎴栫浉閭荤殑鍖洪棿锛堣緭鍏ラ渶宸叉寜 start 鍗囧簭鎺掑簭锛?func mergeIPv4Ranges(ranges []ipRange) []ipRange {
	if len(ranges) == 0 {
		return ranges
	}
	merged := make([]ipRange, 0, len(ranges))
	cur := ranges[0]
	for i := 1; i < len(ranges); i++ {
		r := ranges[i]
		if r.start <= cur.end || (cur.end < ^uint32(0) && r.start == cur.end+1) {
			if r.end > cur.end {
				cur.end = r.end
			}
		} else {
			merged = append(merged, cur)
			cur = r
		}
	}
	merged = append(merged, cur)
	return merged
}

// findIPv4Range 鍦ㄥ凡鎺掑簭涓斾笉閲嶅彔鐨勫尯闂翠腑浜屽垎鏌ユ壘
func findIPv4Range(ranges []ipRange, ip uint32) bool {
	left, right := 0, len(ranges)
	for left < right {
		mid := (left + right) / 2
		r := ranges[mid]
		if ip < r.start {
			right = mid
		} else if ip > r.end {
			left = mid + 1
		} else {
			return true
		}
	}
	return false
}

// loadChinaIPV6List 鍔犺浇涓浗IPv6鍒楄〃锛堜紭鍏堣鍐呯疆鏂囦欢锛屽啀璇?/data 鎸佷箙鍖栨枃浠讹級
func loadChinaIPV6List() error {
	// 鏌ユ壘椤哄簭锛?data/鏇存柊鏂囦欢 > /usr/local/bin/鍐呯疆鏂囦欢 > ./褰撳墠鐩綍
	searchPaths := []string{
		"/data/chn_ip_v6.txt",
		"/usr/local/bin/chn_ip_v6.txt",
		"chn_ip_v6.txt",
	}

	var ipListFile string
	for _, p := range searchPaths {
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			ipListFile = p
			break
		}
	}
	if ipListFile == "" {
		log.Printf("[鍔犺浇] IPv6 鍒楄〃鏂囦欢涓嶅瓨鍦紝灏嗚烦杩?IPv6 鏀寔")
		return nil // IPv6 鍒楄〃涓嶅瓨鍦ㄤ笉绠楄嚧鍛介敊璇?	}

	file, err := os.Open(ipListFile)
	if err != nil {
		// 鏂囦欢鎵撳紑澶辫触锛屼笉绠楄嚧鍛介敊璇?		log.Printf("[璀﹀憡] 鎵撳紑 IPv6 IP鍒楄〃鏂囦欢澶辫触: %v锛屽皢璺宠繃 IPv6 鏀寔", err)
		return nil
	}
	defer file.Close()

	var ranges []ipRangeV6
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)

		// CIDR 鏍煎紡: 2001:250::/35
		if len(parts) == 1 && strings.Contains(parts[0], "/") {
			_, cidr, err := net.ParseCIDR(parts[0])
			if err != nil {
				continue
			}
			startBytes := cidr.IP.To16()
			endBytes := lastIPv6(cidr)
			if startBytes == nil || endBytes == nil {
				continue
			}
			var start, end [16]byte
			copy(start[:], startBytes)
			copy(end[:], endBytes)
			if compareIPv6(start, end) <= 0 {
				ranges = append(ranges, ipRangeV6{start: start, end: end})
			}
			continue
		}

		// "startIP endIP" 鏍煎紡
		if len(parts) < 2 {
			continue
		}

		startIP := net.ParseIP(parts[0])
		endIP := net.ParseIP(parts[1])
		if startIP == nil || endIP == nil {
			continue
		}

		startBytes := startIP.To16()
		endBytes := endIP.To16()
		if startBytes == nil || endBytes == nil {
			continue
		}

		var start, end [16]byte
		copy(start[:], startBytes)
		copy(end[:], endBytes)

		if compareIPv6(start, end) <= 0 {
			ranges = append(ranges, ipRangeV6{start: start, end: end})
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("璇诲彇IPv6 IP鍒楄〃鏂囦欢澶辫触: %w", err)
	}

	if len(ranges) == 0 {
		// IPv6鍒楄〃涓虹┖涓嶇畻閿欒锛屽彲鑳芥枃浠朵笉瀛樺湪鎴栦负绌?		return nil
	}

	// 鎸夎捣濮婭P鎺掑簭
	sort.Slice(ranges, func(i, j int) bool {
		return compareIPv6(ranges[i].start, ranges[j].start) < 0
	})

	chinaIPV6RangesMu.Lock()
	chinaIPV6Ranges = ranges
	chinaIPV6RangesMu.Unlock()

	return nil
}

// lookupIPWithCache 甯︾紦瀛樼殑 DNS 鏌ヨ
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
		expiresAt: time.Now().Add(5 * time.Minute), // 缂撳瓨 5 鍒嗛挓
	}
	// 绠€鍗曟竻鐞嗭細瓒呰繃 1000 鏉℃椂娓呯┖
	if len(dnsCache) > 1000 {
		for k := range dnsCache {
			delete(dnsCache, k)
		}
	}
	dnsCacheMu.Unlock()

	return ips, nil
}

// shouldBypassProxy 鏍规嵁鍒嗘祦妯″紡鍒ゆ柇鏄惁搴旇缁曡繃浠ｇ悊锛堢洿杩烇級
func shouldBypassProxy(targetHost string) bool {
	// 1. 鑷畾涔夎鍒欎紭鍏堢骇鏈€楂橈紙浠讳綍鍒嗘祦妯″紡涓嬮兘鐢熸晥锛?	if matched, bypass := matchCustomRule(targetHost); matched {
		return bypass
	}

	// 2. 鎸夊垎娴佹ā寮忓喅瀹?	switch routingMode {
	case "none":
		// "涓嶆敼鍙樹唬鐞?妯″紡锛氭墍鏈夋祦閲忛兘鐩磋繛
		return true
	case "global":
		// "鍏ㄥ眬浠ｇ悊"妯″紡锛氭墍鏈夋祦閲忛兘璧颁唬鐞?		return false
	case "custom":
		// 鑷畾涔夎鍒欐ā寮忥細鏃犲尮閰嶈鍒欐椂榛樿璧颁唬鐞?		return false
	case "bypass_cn":
		// "璺宠繃涓浗澶ч檰"妯″紡锛氭鏌ユ槸鍚︽槸涓浗IP
		if ip := net.ParseIP(targetHost); ip != nil {
			return isChinaIP(targetHost)
		}
		// 濡傛灉鏄煙鍚嶏紝鍏堣В鏋怚P锛堝甫缂撳瓨锛?		ips, err := lookupIPWithCache(targetHost)
		if err != nil {
			// 瑙ｆ瀽澶辫触锛岄粯璁よ蛋浠ｇ悊
			return false
		}
		// 妫€鏌ユ墍鏈夎В鏋愬埌鐨処P锛屽鏋滄湁涓€涓槸涓浗IP锛屽氨鐩磋繛
		for _, ip := range ips {
			if isChinaIP(ip.String()) {
				return true
			}
		}
		// 閮戒笉鏄腑鍥絀P锛岃蛋浠ｇ悊
		return false
	}
	// 鏈煡妯″紡锛岄粯璁よ蛋浠ｇ悊
	return false
}

// matchCustomRule 鍖归厤鑷畾涔夎鍒欙紝杩斿洖 (鏄惁鍛戒腑, 鏄惁鐩磋繛)
func matchCustomRule(targetHost string) (bool, bool) {
	customRulesMu.RLock()
	defer customRulesMu.RUnlock()

	if len(customRules) == 0 {
		return false, false
	}

	host, _, err := net.SplitHostPort(targetHost)
	if err != nil {
		host = targetHost
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	// 妫€鏌ユ槸鍚︽槸IP鍦板潃
	isIP := net.ParseIP(host) != nil

	for _, rule := range customRules {
		switch rule.Type {
		case "domain":
			if !isIP {
				value := strings.ToLower(strings.TrimSpace(rule.Value))
				matched := false
				// 鏀寔閫氶厤绗?*.domain
				if strings.HasPrefix(value, "*.") {
					suffix := strings.TrimPrefix(value, "*.")
					if host == suffix || strings.HasSuffix(host, "."+suffix) {
						matched = true
					}
				} else {
					// 绮剧‘鍖归厤鎴栧瓙鍩熷悕鍖归厤
					if host == value || strings.HasSuffix(host, "."+value) {
						matched = true
					}
				}
				if matched {
					return true, rule.Action == "direct"
				}
			}
		case "ipcidr":
			if isIP {
				ip := net.ParseIP(host)
				if ip != nil {
					_, cidr, err := net.ParseCIDR(rule.Value)
					if err == nil && cidr.Contains(ip) {
						return true, rule.Action == "direct"
					}
				}
			}
		case "keyword":
			if rule.Value != "" && strings.Contains(host, strings.ToLower(rule.Value)) {
				return true, rule.Action == "direct"
			}
		}
	}
	// 娌℃湁鍖归厤瑙勫垯
	return false, false
}

func isNormalCloseError(err error) bool {
	if err == nil {
		return false
	}
	if err == io.EOF {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "normal closure")
}

// ======================== 鑷畾涔夎鍒?========================

// loadCustomRules 浠庢枃浠跺姞杞借嚜瀹氫箟瑙勫垯
func loadCustomRules(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("鎵撳紑瑙勫垯鏂囦欢澶辫触: %w", err)
	}
	defer file.Close()

	var rules []customRule
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
		if action != "proxy" && action != "direct" {
			continue
		}

		rules = append(rules, customRule{
			Type:   ruleType,
			Value:  value,
			Action: action,
		})
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("璇诲彇瑙勫垯鏂囦欢澶辫触: %w", err)
	}

	customRulesMu.Lock()
	customRules = rules
	customRulesMu.Unlock()

	return nil
}

// saveCustomRules 淇濆瓨鑷畾涔夎鍒欏埌鏂囦欢
func saveCustomRules(filePath string) error {
	customRulesMu.RLock()
	rules := make([]customRule, len(customRules))
	copy(rules, customRules)
	customRulesMu.RUnlock()

	// 纭繚鐩綍瀛樺湪
	if dir := filepath.Dir(filePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("鍒涘缓瑙勫垯鐩綍澶辫触: %w", err)
		}
	}

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("鍒涘缓瑙勫垯鏂囦欢澶辫触: %w", err)
	}
	defer file.Close()

	for _, rule := range rules {
		fmt.Fprintf(file, "%s,%s,%s\n", rule.Type, rule.Value, rule.Action)
	}

	return nil
}

// loadRulesDataFile 浠庨潰鏉胯鍒欐寔涔呭寲鏂囦欢鍔犺浇瑙勫垯
func loadRulesDataFile() {
	if rulesData == "" {
		return
	}
	if _, err := os.Stat(rulesData); os.IsNotExist(err) {
		return
	}
	if err := loadCustomRules(rulesData); err != nil {
		log.Printf("[瑙勫垯] 鍔犺浇闈㈡澘瑙勫垯澶辫触: %v", err)
	} else {
		customRulesMu.RLock()
		log.Printf("[瑙勫垯] 宸蹭粠闈㈡澘瑙勫垯鏂囦欢鍔犺浇 %d 鏉¤鍒?, len(customRules))
		customRulesMu.RUnlock()
	}
}

// ======================== 杩炴帴杩借釜涓庢祦閲忕粺璁?========================

// addConn 娣诲姞杩炴帴杩借釜
func addConn(source, target, mode, rule string) string {
	id := fmt.Sprintf("%d", connIDCounter.Add(1))
	info := &connInfo{
		ID:        id,
		Source:    source,
		Target:    target,
		Mode:      mode,
		Rule:      rule,
		StartTime: time.Now(),
	}
	activeConns.Store(id, info)
	activeConnCnt.Add(1)
	totalConns.Add(1)
	return id
}

// removeConn 绉婚櫎杩炴帴杩借釜锛屽皢娴侀噺璁″叆鎬荤粺璁?func removeConn(id string) {
	if v, ok := activeConns.LoadAndDelete(id); ok {
		activeConnCnt.Add(-1)
		info := v.(*connInfo)
		up := info.upload.Load()
		dn := info.download.Load()
		totalUpload.Add(up)
		totalDownload.Add(dn)
	}
}

// getActiveConns 鑾峰彇鎵€鏈夋椿璺冭繛鎺?func getActiveConns() []connInfoResp {
	var conns []connInfoResp
	activeConns.Range(func(key, value interface{}) bool {
		info := value.(*connInfo)
		conns = append(conns, connInfoResp{
			ID:        info.ID,
			Source:    info.Source,
			Target:    info.Target,
			Mode:      info.Mode,
			Rule:      info.Rule,
			Upload:    info.upload.Load(),
			Download:  info.download.Load(),
			StartTime: info.StartTime,
		})
		return true
	})
	return conns
}

// addLog 娣诲姞鏃ュ織鍒扮紦鍐?func addLog(level, msg string) {
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

// ======================== Web 绠＄悊闈㈡澘 ========================

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// startWebServer 鍚姩 Web 绠＄悊闈㈡澘
func startWebServer(addr string) {
	mux := http.NewServeMux()

	// 鐧诲綍/鐧诲嚭锛堜笉闇€閴存潈锛?	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/api/login", handleAPILogin)
	mux.HandleFunc("/api/logout", handleAPILogout)

	// 闇€瑕侀壌鏉冪殑璺敱
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

	log.Printf("[Web] 绠＄悊闈㈡澘鍚姩: http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("[Web] 绠＄悊闈㈡澘鍚姩澶辫触: %v", err)
	}
}

// isWebAuthenticated 妫€鏌ュ綋鍓嶈姹傛槸鍚﹀凡鐧诲綍
func isWebAuthenticated(r *http.Request) bool {
	if webPassword == "" {
		return true
	}
	cookie, err := r.Cookie("ech_session")
	if err != nil || cookie.Value == "" {
		return false
	}
	webSessionsMu.Lock()
	defer webSessionsMu.Unlock()
	exp, ok := webSessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(webSessions, cookie.Value)
		return false
	}
	return true
}

// newSessionID 鐢熸垚涓嶅彲棰勬祴鐨?session ID
func newSessionID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 鏋佸皬姒傜巼闄嶇骇锛氱撼绉?+ pid锛屼絾浠嶅敖閲忎笉鍙娴?		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	return hex.EncodeToString(b[:])
}

// clearExpiredSessions 瀹氭湡娓呯悊杩囨湡 session锛岄伩鍏嶅唴瀛樻硠婕?func startSessionGC() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		webSessionsMu.Lock()
		for k, exp := range webSessions {
			if now.After(exp) {
				delete(webSessions, k)
			}
		}
		webSessionsMu.Unlock()
	}
}

// authMiddleware 閴存潈涓棿浠讹紙webPassword 涓虹┖鏃惰烦杩囬壌鏉冿級
// API/WS 鏈櫥褰曡繑鍥?401 JSON锛堥伩鍏嶅墠绔?fetch 鎷垮埌 302 HTML 瑙ｆ瀽澶辫触锛夛紝椤甸潰鎵?302 璺崇櫥褰曢〉
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}

		// 娓呮帀澶辨晥 cookie锛岄伩鍏嶆祻瑙堝櫒涓€鐩村甫鏃犳晥 session
		if _, err := r.Cookie("ech_session"); err == nil {
			http.SetCookie(w, &http.Cookie{
				Name:     "ech_session",
				Value:    "",
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   -1,
			})
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "鏈櫥褰?})
			return
		}
		if r.URL.Path == "/ws" {
			// WebSocket 鎻℃墜涓嶈兘 302锛岀洿鎺?401 璁╁墠绔烦鐧诲綍
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "鏈櫥褰?})
			return
		}

		// 鏈櫥褰曪紝閲嶅畾鍚戝埌鐧诲綍椤?		http.Redirect(w, r, "/login", http.StatusFound)
	})
}

// handleLogin 鎻愪緵鐧诲綍椤甸潰
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if webPassword == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginHTML))
}

// loginHTML 鐧诲綍椤甸潰锛堜笌 index.html 鍚屼竴濂椾富棰樺彉閲忥級
var loginHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ECH Proxy Control - 鐧诲綍</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Syne:wght@600;700;800&family=IBM+Plex+Mono:wght@400;500;600&family=Inter:wght@400;500;600&display=swap" rel="stylesheet">
<style>
:root{
  --bg-0:#070b14;
  --bg-1:#0c1220;
  --bg-2:#111a2e;
  --glass:rgba(18,27,48,0.6);
  --glass-strong:rgba(22,33,58,0.85);
  --border:rgba(120,180,255,0.12);
  --border-hover:rgba(120,180,255,0.28);
  --text:#e8eefc;
  --text-sec:#8b99b8;
  --text-dim:#4a5678;
  --cyan:#22d3ee;
  --cyan-glow:rgba(34,211,238,0.4);
  --green:#34d399;
  --red:#fb7185;
  --radius:14px;
  --radius-sm:10px;
}
*{margin:0;padding:0;box-sizing:border-box}
html,body{height:100%}
body{font-family:'Inter',-apple-system,sans-serif;background:var(--bg-0);color:var(--text);min-height:100vh;display:flex;align-items:center;justify-content:center;position:relative;overflow:hidden}
body::before{content:'';position:fixed;inset:0;z-index:0;pointer-events:none;background:radial-gradient(ellipse 80% 50% at 20% -10%, rgba(34,211,238,0.12), transparent 60%),radial-gradient(ellipse 60% 40% at 100% 0%, rgba(167,139,250,0.08), transparent 50%),radial-gradient(ellipse 50% 50% at 50% 100%, rgba(52,211,153,0.05), transparent 60%)}
body::after{content:'';position:fixed;inset:0;z-index:0;pointer-events:none;opacity:0.4;background-image:linear-gradient(rgba(120,180,255,0.03) 1px,transparent 1px),linear-gradient(90deg,rgba(120,180,255,0.03) 1px,transparent 1px);background-size:48px 48px;mask-image:radial-gradient(ellipse 70% 60% at 50% 40%,#000 30%,transparent 80%)}
.login-wrap{position:relative;z-index:1;width:380px;max-width:calc(100vw - 32px)}
.login-box{background:var(--glass);backdrop-filter:blur(12px);border:1px solid var(--border);border-radius:var(--radius);padding:32px 30px;box-shadow:0 10px 40px rgba(0,0,0,0.4);position:relative;overflow:hidden}
.login-box::before{content:'';position:absolute;top:0;left:0;right:0;height:1px;background:linear-gradient(90deg,transparent,rgba(34,211,238,0.4),transparent)}
.brand{display:flex;align-items:center;gap:12px;justify-content:center;margin-bottom:22px}
.brand-logo{width:38px;height:38px;border-radius:11px;background:linear-gradient(135deg,var(--cyan),#0891b2);display:flex;align-items:center;justify-content:center;box-shadow:0 0 24px var(--cyan-glow),inset 0 1px 0 rgba(255,255,255,0.2);color:#04121a}
.brand-logo svg{width:20px;height:20px;stroke:currentColor;fill:none}
.brand-title{font-family:'Syne',sans-serif;font-weight:800;font-size:17px;letter-spacing:-0.02em;text-align:left}
.brand-sub{font-size:10px;color:var(--text-dim);letter-spacing:0.08em;text-transform:uppercase;margin-top:2px}
.login-box input{width:100%;padding:10px 14px;background:rgba(7,11,20,0.6);border:1px solid var(--border);border-radius:var(--radius-sm);color:var(--text);font-size:13px;font-family:inherit;margin-bottom:14px;outline:none;transition:all .18s}
.login-box input:focus{border-color:var(--cyan);box-shadow:0 0 0 3px rgba(34,211,238,0.1)}
.login-box button{width:100%;padding:10px;background:linear-gradient(135deg,var(--cyan),#0891b2);border:none;color:#04121a;border-radius:var(--radius-sm);font-size:13px;font-weight:600;cursor:pointer;box-shadow:0 4px 20px var(--cyan-glow);transition:all .18s;font-family:inherit}
.login-box button:hover{filter:brightness(1.1);transform:translateY(-1px)}
.login-box button:disabled{opacity:.6;cursor:default;transform:none}
.error{color:var(--red);text-align:center;margin-bottom:12px;font-size:12px;display:none;font-family:'IBM Plex Mono',monospace}
.hint{text-align:center;margin-top:14px;font-size:11px;color:var(--text-dim)}
</style>
</head>
<body>
<div class="login-wrap">
<div class="login-box">
<div class="brand">
<div class="brand-logo"><svg viewBox="0 0 24 24" stroke-width="2"><path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg></div>
<div><div class="brand-title">ECH Proxy</div><div class="brand-sub">Control Center</div></div>
</div>
<div class="error" id="err"></div>
<input type="password" id="pw" placeholder="璇疯緭鍏ョ鐞嗗瘑鐮? autofocus autocomplete="current-password">
<button id="loginBtn" onclick="doLogin()">鐧?褰?/button>
<div class="hint">Control Center 路 涓庡唴椤靛悓涓€涓婚</div>
</div>
</div>
<script>
document.getElementById('pw').addEventListener('keydown',e=>{if(e.key==='Enter')doLogin()});
async function doLogin(){
const pw=document.getElementById('pw').value;
const err=document.getElementById('err');
const btn=document.getElementById('loginBtn');
err.style.display='none';
btn.disabled=true;
try{
const r=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:pw})});
const d=await r.json();
if(d.status==='ok'){location.href='/'}else{err.textContent=d.error||'瀵嗙爜閿欒';err.style.display='block'}
}catch(e){err.textContent='缃戠粶閿欒';err.style.display='block'}
btn.disabled=false;
}
</script>
</body>
</html>`

// handleAPILogin 澶勭悊鐧诲綍璇锋眰
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
		json.NewEncoder(w).Encode(map[string]string{"error": "璇锋眰鏍煎紡閿欒"})
		return
	}
	if req.Password != webPassword {
		json.NewEncoder(w).Encode(map[string]string{"error": "瀵嗙爜閿欒"})
		return
	}
	// 鐢熸垚 session锛堜笉鍙娴嬶級
	sessionID := newSessionID()
	webSessionsMu.Lock()
	webSessions[sessionID] = time.Now().Add(7 * 24 * time.Hour)
	webSessionsMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "ech_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleAPILogout 閫€鍑虹櫥褰曪紙API 杩斿洖 JSON锛屽墠绔啀璺?/login锛?func handleAPILogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("ech_session"); err == nil {
		webSessionsMu.Lock()
		delete(webSessions, cookie.Value)
		webSessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "ech_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleIndex 鎻愪緵 index.html
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

// handleStatus 鑾峰彇绯荤粺鐘舵€?func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	customRulesMu.RLock()
	customRulesCount := len(customRules)
	customRulesMu.RUnlock()
	json.NewEncoder(w).Encode(statusResponse{
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

// handleConfig 鑾峰彇/鏇存柊閰嶇疆
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
			"proxy_ip":     proxyIP,
			"web_password": webPassword,
		}
		json.NewEncoder(w).Encode(config)
		return
	}

	// POST: 鏇存柊閰嶇疆
	var update map[string]string
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, `{"error":"invalid json"}`, 400)
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
	// dns_server / ech_domain 涓嶅厑璁歌淇濆瓨鎴愮┖鍊硷細绌哄€间細璁?DoH 璇锋眰鍙樻垚
	// "https:?dns=..."锛坣o Host in request URL锛夛紝ECH 閰嶇疆浠庢鍒锋柊澶辫触銆?	// 闈㈡澘琛ㄥ崟鍦ㄦ湭鐧诲綍鎴栨湭鍔犺浇瀹屾垚鏃跺瓧娈垫槸绌虹殑锛屼竴鎶婁繚瀛樺氨浼氭妸杩欎袱椤规竻绌?鈥斺€?	// 杩欐槸"淇濆瓨涓€娆′箣鍚庢暣涓唬鐞嗕笉鑳界敤"鐨勭湡瀹炰簨鏁呰矾寰勶紝蹇呴』闃蹭綇銆?	if v, ok := update["dns_server"]; ok && strings.TrimSpace(v) != "" {
		dnsServer = strings.TrimSpace(v)
	}
	if v, ok := update["ech_domain"]; ok && strings.TrimSpace(v) != "" {
		echDomain = strings.TrimSpace(v)
	}
	if v, ok := update["proxy_ip"]; ok {
		proxyIP = v
		log.Printf("[閰嶇疆] 鍑哄彛 IP 宸叉洿鏂? %s", proxyIP)
	}
	if v, ok := update["web_password"]; ok {
		webPassword = v
		log.Printf("[閰嶇疆] 闈㈡澘瀵嗙爜宸叉洿鏂?)
	}
	if v, ok := update["routing_mode"]; ok && v != "" {
		switch v {
		case "global", "bypass_cn", "none", "custom":
			routingMode = v
			log.Printf("[閰嶇疆] 鍒嗘祦妯″紡宸叉洿鏂? %s", routingMode)
		}
	}
	if v, ok := update["web_addr"]; ok && v != "" {
		webAddr = v
	}

	// 鍒锋柊 ECH
	if err := refreshECH(); err != nil {
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 淇濆瓨閰嶇疆鍒版枃浠?	if err := saveConfig(configFile); err != nil {
		log.Printf("[閰嶇疆] 淇濆瓨閰嶇疆鏂囦欢澶辫触: %v", err)
	} else {
		log.Printf("[閰嶇疆] 閰嶇疆宸蹭繚瀛樺埌: %s", configFile)
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleRules 鑾峰彇/鏇存柊鑷畾涔夎鍒?func handleRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "GET" {
		customRulesMu.RLock()
		rules := make([]customRule, len(customRules))
		copy(rules, customRules)
		customRulesMu.RUnlock()
		json.NewEncoder(w).Encode(rules)
		return
	}

	// POST: 鏇挎崲瑙勫垯
	var rules []customRule
	if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
		http.Error(w, `{"error":"invalid json"}`, 400)
		return
	}

	customRulesMu.Lock()
	customRules = rules
	customRulesMu.Unlock()

	// 鎸佷箙鍖栧埌鏂囦欢
	if err := saveCustomRules(rulesData); err != nil {
		log.Printf("[瑙勫垯] 淇濆瓨瑙勫垯澶辫触: %v", err)
	} else {
		log.Printf("[瑙勫垯] 宸蹭繚瀛?%d 鏉¤鍒欏埌 %s", len(rules), rulesData)
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleConnections 鑾峰彇娲昏穬杩炴帴锛堟敮鎸佹悳绱㈠拰鎺掑簭锛?func handleConnections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	conns := getActiveConns()

	// 鎼滅储杩囨护
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	if search != "" {
		var filtered []connInfoResp
		for _, c := range conns {
			if strings.Contains(strings.ToLower(c.ID), search) ||
				strings.Contains(strings.ToLower(c.Source), search) ||
				strings.Contains(strings.ToLower(c.Target), search) ||
				strings.Contains(strings.ToLower(c.Mode), search) ||
				strings.Contains(strings.ToLower(c.Rule), search) {
				filtered = append(filtered, c)
			}
		}
		conns = filtered
	}

	// 鎺掑簭
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

// handleTraffic 鑾峰彇娴侀噺缁熻
func handleTraffic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total_upload":   totalUpload.Load(),
		"total_download": totalDownload.Load(),
		"total_conns":    totalConns.Load(),
		"uptime":         time.Since(startTime).Seconds(),
	})
}

// handleLogs 鑾峰彇鏃ュ織锛堟寜鏃堕棿姝ｅ簭杩斿洖鏈€杩?100 鏉★紝鍓嶇鐩存帴娓叉煋骞舵粴鍒板簳锛?func handleLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	logBufferMu.Lock()
	logs := make([]logEntry, len(logBuffer))
	copy(logs, logBuffer)
	logBufferMu.Unlock()

	// logBuffer 鏈韩宸叉槸鏃堕棿姝ｅ簭锛屽彧鍙栧熬閮ㄦ渶杩?100 鏉★紝淇濇寔姝ｅ簭
	if len(logs) > 100 {
		logs = logs[len(logs)-100:]
	}
	if logs == nil {
		logs = []logEntry{}
	}
	json.NewEncoder(w).Encode(logs)
}

// handleService 鏈嶅姟鎺у埗
func handleService(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	action := strings.TrimPrefix(r.URL.Path, "/api/service/")

	switch action {
	case "refresh-ech":
		if err := refreshECH(); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "ECH 閰嶇疆宸插埛鏂?})
	case "reload-rules":
		if rulesFile == "" {
			json.NewEncoder(w).Encode(map[string]string{"error": "鏈厤缃鍒欐枃浠?})
			return
		}
		if err := loadCustomRules(rulesFile); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "瑙勫垯宸查噸杞?})
	default:
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown action"})
	}
}

// handleWebSocket WebSocket 瀹炴椂鎺ㄩ€?// ======================== 鍑哄彛IP妫€娴?========================

var (
	cachedExitIP   string
	cachedCOLO     string
	exitInfoMu     sync.RWMutex
	exitInfoOnce   sync.Once
)

func startExitInfoDetector() {
	go func() {
		time.Sleep(5 * time.Second)
		detectExitInfo()
		for {
			time.Sleep(60 * time.Second)
			detectExitInfo()
		}
	}()
}

func detectExitInfo() {
	log.Printf("[妫€娴媇 === 寮€濮嬫娴嬪嚭鍙P ===")

	// 閫氳繃鏈湴 HTTP 浠ｇ悊妫€娴嬪嚭鍙?IP锛堥伩鍏?Worker 涓嶆敮鎸?80 绔彛鐨勯棶棰橈級
	localProxy := "http://127.0.0.1" + listenAddr[strings.Index(listenAddr, ":"):]
	proxyURL, _ := url.Parse(localProxy)

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	// 妫€娴嬪嚭鍙?IP
	exitIP := ""
	resp, err := client.Get("https://api.ipify.org?format=json")
	if err != nil {
		log.Printf("[妫€娴媇 鑾峰彇鍑哄彛IP澶辫触: %v", err)
	} else {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var ipResp struct{ IP string `json:"ip"` }
		if json.Unmarshal(body, &ipResp) == nil {
			exitIP = ipResp.IP
		}
	}

	// 妫€娴?COLO锛堥€氳繃 Cloudflare trace锛?	colo := ""
	resp2, err := client.Get("https://1.1.1.1/cdn-cgi/trace")
	if err == nil {
		defer resp2.Body.Close()
		body, _ := io.ReadAll(resp2.Body)
		traceStr := string(body)
		if idx := strings.Index(traceStr, "colo="); idx >= 0 {
			start := idx + 5
			end := start
			for end < len(traceStr) && traceStr[end] != '\r' && traceStr[end] != '\n' {
				end++
			}
			colo = traceStr[start:end]
		}
	}

	exitInfoMu.Lock()
	cachedExitIP = exitIP
	cachedCOLO = colo
	exitInfoMu.Unlock()

	if exitIP != "" {
		log.Printf("[妫€娴媇 鍑哄彛IP: %s, COLO: %s", exitIP, colo)
	}
}

// handleExitInfo 杩斿洖缂撳瓨鐨勫嚭鍙ｄ俊鎭?func handleExitInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	exitInfoMu.RLock()
	defer exitInfoMu.RUnlock()
	json.NewEncoder(w).Encode(map[string]string{
		"exit_ip": cachedExitIP,
		"colo":    cachedCOLO,
	})
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Web] WebSocket 鍗囩骇澶辫触: %v", err)
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// 鐢ㄤ簬璁＄畻瀹炴椂閫熷害锛堝崟浣?B/s锛?	var prevUp, prevDn int64
	var prevTime time.Time
	firstTick := true

	for {
		select {
		case <-ticker.C:
			// 璁＄畻瀹炴椂娴侀噺锛堟椿璺冭繛鎺ョ殑娴侀噺涔嬪拰锛?			var rtUp, rtDn int64
			activeConns.Range(func(key, value interface{}) bool {
				info := value.(*connInfo)
				rtUp += info.upload.Load()
				rtDn += info.download.Load()
				return true
			})

			// 璁＄畻澧為噺閫熷害锛堟€讳笂浼?涓嬭浇 - 涓婃鍊硷級/ 瀹為檯闂撮殧绉掓暟
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

// ======================== ECH 鏀寔 ========================

const typeHTTPS = 65

// 鍏ㄥ眬 HTTP 瀹㈡埛绔紝澶嶇敤 TCP 杩炴帴
var dohHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		MaxIdleConns:      10,
		IdleConnTimeout:   90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

func prepareECH() error {
	// 濡傛灉閰嶇疆浜嗙幆澧冧唬鐞嗭紝璺宠繃 ECH锛堜唬鐞嗙幆澧冧笅 ECH 鍙兘涓嶅吋瀹癸級
	if hasEnvProxy() {
		log.Printf("[ECH] 妫€娴嬪埌鐜浠ｇ悊锛岃烦杩?ECH 閰嶇疆鑾峰彇")
		return nil
	}

	echBase64, err := queryHTTPSRecord(echDomain, dnsServer)
	if err != nil {
		return fmt.Errorf("DNS 鏌ヨ澶辫触: %w", err)
	}
	if echBase64 == "" {
		return errors.New("鏈壘鍒?ECH 鍙傛暟")
	}
	raw, err := base64.StdEncoding.DecodeString(echBase64)
	if err != nil {
		return fmt.Errorf("ECH 瑙ｇ爜澶辫触: %w", err)
	}
	echListMu.Lock()
	echList = raw
	echListMu.Unlock()
	log.Printf("[ECH] 閰嶇疆宸插姞杞斤紝闀垮害: %d 瀛楄妭", len(raw))
	return nil
}

func refreshECH() error {
	log.Printf("[ECH] 鍒锋柊閰嶇疆...")
	return prepareECH()
}

func getECHList() ([]byte, error) {
	echListMu.RLock()
	defer echListMu.RUnlock()
	if len(echList) == 0 {
		return nil, errors.New("ECH 閰嶇疆鏈姞杞?)
	}
	return echList, nil
}

func buildTLSConfigWithECH(serverName string, echList []byte) (*tls.Config, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("鍔犺浇绯荤粺鏍硅瘉涔﹀け璐? %w", err)
	}

	if echList == nil || len(echList) == 0 {
		return nil, errors.New("ECH 閰嶇疆涓虹┖锛岃繖鏄繀闇€鍔熻兘")
	}

	config := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: serverName,
		RootCAs:    roots,
	}

	// 浣跨敤鍙嶅皠璁剧疆 ECH 瀛楁锛圗CH 鏄牳蹇冨姛鑳斤紝蹇呴』璁剧疆鎴愬姛锛?	if err := setECHConfig(config, echList); err != nil {
		return nil, fmt.Errorf("璁剧疆 ECH 閰嶇疆澶辫触锛堥渶瑕?Go 1.23+ 鎴栨敮鎸?ECH 鐨勭増鏈級: %w", err)
	}

	return config, nil
}

// setECHConfig 浣跨敤鍙嶅皠璁剧疆 ECH 閰嶇疆锛圗CH 鏄牳蹇冨姛鑳斤紝蹇呴』鎴愬姛锛?func setECHConfig(config *tls.Config, echList []byte) error {
	configValue := reflect.ValueOf(config).Elem()

	// 璁剧疆 EncryptedClientHelloConfigList锛堝繀闇€锛?	field1 := configValue.FieldByName("EncryptedClientHelloConfigList")
	if !field1.IsValid() || !field1.CanSet() {
		return fmt.Errorf("EncryptedClientHelloConfigList 瀛楁涓嶅彲鐢紝闇€瑕?Go 1.23+ 鐗堟湰")
	}
	field1.Set(reflect.ValueOf(echList))

	// 璁剧疆 EncryptedClientHelloRejectionVerify锛堝繀闇€锛?	field2 := configValue.FieldByName("EncryptedClientHelloRejectionVerify")
	if !field2.IsValid() || !field2.CanSet() {
		return fmt.Errorf("EncryptedClientHelloRejectionVerify 瀛楁涓嶅彲鐢紝闇€瑕?Go 1.23+ 鐗堟湰")
	}
	rejectionFunc := func(cs tls.ConnectionState) error {
		return errors.New("鏈嶅姟鍣ㄦ嫆缁?ECH")
	}
	field2.Set(reflect.ValueOf(rejectionFunc))

	return nil
}

// queryHTTPSRecord 閫氳繃 DoH 鏌ヨ HTTPS 璁板綍
func queryHTTPSRecord(domain, dnsServer string) (string, error) {
	dohURL := dnsServer
	if !strings.HasPrefix(dohURL, "https://") && !strings.HasPrefix(dohURL, "http://") {
		dohURL = "https://" + dohURL
	}
	return queryDoH(domain, dohURL)
}

// queryDoH 鎵ц DoH 鏌ヨ锛堢敤浜庤幏鍙?ECH 閰嶇疆锛?func queryDoH(domain, dohURL string) (string, error) {
	u, err := url.Parse(dohURL)
	if err != nil {
		return "", fmt.Errorf("鏃犳晥鐨?DoH URL: %v", err)
	}

	dnsQuery := buildDNSQuery(domain, typeHTTPS)
	dnsBase64 := base64.RawURLEncoding.EncodeToString(dnsQuery)

	q := u.Query()
	q.Set("dns", dnsBase64)
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("鍒涘缓璇锋眰澶辫触: %v", err)
	}
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("Content-Type", "application/dns-message")

	resp, err := dohHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("DoH 璇锋眰澶辫触: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DoH 鏈嶅姟鍣ㄨ繑鍥為敊璇? %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("璇诲彇 DoH 鍝嶅簲澶辫触: %v", err)
	}

	return parseDNSResponse(body)
}

func buildDNSQuery(domain string, qtype uint16) []byte {
	query := make([]byte, 0, 512)
	query = append(query, 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	for _, label := range strings.Split(domain, ".") {
		query = append(query, byte(len(label)))
		query = append(query, []byte(label)...)
	}
	query = append(query, 0x00, byte(qtype>>8), byte(qtype), 0x00, 0x01)
	return query
}

func parseDNSResponse(response []byte) (string, error) {
	if len(response) < 12 {
		return "", errors.New("鍝嶅簲杩囩煭")
	}
	ancount := binary.BigEndian.Uint16(response[6:8])
	if ancount == 0 {
		return "", errors.New("鏃犲簲绛旇褰?)
	}

	offset := 12
	for offset < len(response) && response[offset] != 0 {
		offset += int(response[offset]) + 1
	}
	offset += 5

	for i := 0; i < int(ancount); i++ {
		if offset >= len(response) {
			break
		}
		if response[offset]&0xC0 == 0xC0 {
			offset += 2
		} else {
			for offset < len(response) && response[offset] != 0 {
				offset += int(response[offset]) + 1
			}
			offset++
		}
		if offset+10 > len(response) {
			break
		}
		rrType := binary.BigEndian.Uint16(response[offset : offset+2])
		offset += 8
		dataLen := binary.BigEndian.Uint16(response[offset : offset+2])
		offset += 2
		if offset+int(dataLen) > len(response) {
			break
		}
		data := response[offset : offset+int(dataLen)]
		offset += int(dataLen)

		if rrType == typeHTTPS {
			if ech := parseHTTPSRecord(data); ech != "" {
				return ech, nil
			}
		}
	}
	return "", nil
}

func parseHTTPSRecord(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	offset := 2
	if offset < len(data) && data[offset] == 0 {
		offset++
	} else {
		for offset < len(data) && data[offset] != 0 {
			offset += int(data[offset]) + 1
		}
		offset++
	}
	for offset+4 <= len(data) {
		key := binary.BigEndian.Uint16(data[offset : offset+2])
		length := binary.BigEndian.Uint16(data[offset+2 : offset+4])
		offset += 4
		if offset+int(length) > len(data) {
			break
		}
		value := data[offset : offset+int(length)]
		offset += int(length)
		if key == 5 {
			return base64.StdEncoding.EncodeToString(value)
		}
	}
	return ""
}

// ======================== DoH 浠ｇ悊鏀寔 ========================

// queryDoHForProxy 閫氳繃 ECH 杞彂 DNS 鏌ヨ鍒?Cloudflare DoH
func queryDoHForProxy(dnsQuery []byte) ([]byte, error) {
	_, port, _, err := parseServerAddr(serverAddr)
	if err != nil {
		return nil, err
	}

	// 鏋勫缓 DoH URL
	dohURL := fmt.Sprintf("https://cloudflare-dns.com:%s/dns-query", port)

	echBytes, err := getECHList()
	if err != nil {
		return nil, fmt.Errorf("鑾峰彇 ECH 閰嶇疆澶辫触: %w", err)
	}

	tlsCfg, err := buildTLSConfigWithECH("cloudflare-dns.com", echBytes)
	if err != nil {
		return nil, fmt.Errorf("鏋勫缓 TLS 閰嶇疆澶辫触: %w", err)
	}

	// 鍒涘缓 HTTP 瀹㈡埛绔?	transport := &http.Transport{
		TLSClientConfig: tlsCfg,
	}

	// 濡傛灉鎸囧畾浜?IP锛屼娇鐢ㄨ嚜瀹氫箟 Dialer
	if serverIP != "" {
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			dialer := &net.Dialer{
				Timeout: 10 * time.Second,
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(serverIP, port))
		}
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}

	// 鍙戦€?DoH 璇锋眰
	req, err := http.NewRequest("POST", dohURL, bytes.NewReader(dnsQuery))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DoH 璇锋眰澶辫触: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH 鍝嶅簲閿欒: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// ======================== WebSocket 瀹㈡埛绔?========================

// getEffectiveServerAddr 鑾峰彇甯?ProxyIP 鐨勬湁鏁堟湇鍔″櫒鍦板潃
func getEffectiveServerAddr() string {
	if proxyIP == "" {
		return serverAddr
	}
	// 鍒嗙 host:port 鍜?path
	host, port, path, err := parseServerAddr(serverAddr)
	if err != nil {
		return serverAddr
	}
	// 纭繚 proxyIP 鍖呭惈绔彛
	proxyHost := proxyIP
	if !strings.Contains(proxyIP, ":") {
		proxyHost = proxyIP + ":443"
	}
	// 鏋勫缓鏂?path
	if path == "" || path == "/" {
		path = "/?ip=" + proxyHost
	} else {
		if strings.Contains(path, "?") {
			path = path + "&ip=" + proxyHost
		} else {
			path = path + "?ip=" + proxyHost
		}
	}
	return fmt.Sprintf("%s:%s%s", host, port, path)
}

func parseServerAddr(addr string) (host, port, path string, err error) {
	path = "/"
	slashIdx := strings.Index(addr, "/")
	if slashIdx != -1 {
		path = addr[slashIdx:]
		addr = addr[:slashIdx]
	}

	host, port, err = net.SplitHostPort(addr)
	if err != nil {
		return "", "", "", fmt.Errorf("鏃犳晥鐨勬湇鍔″櫒鍦板潃鏍煎紡: %v", err)
	}

	return host, port, path, nil
}

// hasEnvProxy 妫€娴嬫槸鍚﹂厤缃簡鐜浠ｇ悊
func hasEnvProxy() bool {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := os.Getenv(key); v != "" {
			return true
		}
	}
	return false
}

func dialWebSocketWithECH(maxRetries int) (*websocket.Conn, error) {
	effectiveAddr := getEffectiveServerAddr()
	host, port, path, err := parseServerAddr(effectiveAddr)
	if err != nil {
		return nil, err
	}

	wsURL := fmt.Sprintf("wss://%s:%s%s", host, port, path)
	useProxy := hasEnvProxy()

	for attempt := 1; attempt <= maxRetries; attempt++ {
		var tlsCfg *tls.Config

		if useProxy {
			// 閫氳繃鐜浠ｇ悊杩炴帴鏃惰烦杩?ECH锛圗CH 鍦ㄤ唬鐞嗛毀閬撲腑鍙兘涓嶅吋瀹癸級
			tlsCfg = &tls.Config{
				ServerName: host,
				MinVersion: tls.VersionTLS13,
			}
			if attempt == 1 {
				log.Printf("[浠ｇ悊] 妫€娴嬪埌鐜浠ｇ悊锛岃烦杩?ECH 鐩存帴杩炴帴")
			}
		} else {
			echBytes, echErr := getECHList()
			if echErr != nil {
				if attempt < maxRetries {
					refreshECH()
					continue
				}
				return nil, echErr
			}

			tlsCfg, err = buildTLSConfigWithECH(host, echBytes)
			if err != nil {
				return nil, err
			}
		}

		dialer := websocket.Dialer{
			TLSClientConfig: tlsCfg,
			Subprotocols: func() []string {
				if token == "" {
					return nil
				}
				return []string{token}
			}(),
			HandshakeTimeout: 10 * time.Second,
			Proxy:            http.ProxyFromEnvironment,
		}

		// 鍙湁鍦ㄦ病鏈夐厤缃幆澧冧唬鐞嗘椂鎵嶄娇鐢ㄨ嚜瀹氫箟鎷ㄥ彿杩炴帴鍥哄畾 IP
		if serverIP != "" && !useProxy {
			dialer.NetDial = func(network, address string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				return net.DialTimeout(network, net.JoinHostPort(serverIP, port), 10*time.Second)
			}
		}

		wsConn, _, dialErr := dialer.Dial(wsURL, nil)
		if dialErr != nil {
			if !useProxy && strings.Contains(dialErr.Error(), "ECH") && attempt < maxRetries {
				log.Printf("[ECH] 杩炴帴澶辫触锛屽皾璇曞埛鏂伴厤缃?(%d/%d)", attempt, maxRetries)
				refreshECH()
				time.Sleep(time.Second)
				continue
			}
			return nil, dialErr
		}

		return wsConn, nil
	}

	return nil, errors.New("杩炴帴澶辫触锛屽凡杈炬渶澶ч噸璇曟鏁?)
}

// ======================== 缁熶竴浠ｇ悊鏈嶅姟鍣?========================

func runProxyServer(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("[浠ｇ悊] 鐩戝惉澶辫触: %v", err)
	}
	defer listener.Close()

	log.Printf("[浠ｇ悊] 鏈嶅姟鍣ㄥ惎鍔? %s (鏀寔 SOCKS5 鍜?HTTP)", addr)
	log.Printf("[浠ｇ悊] 鍚庣鏈嶅姟鍣? %s", serverAddr)
	if downLimitMbps > 0 {
		log.Printf("[浠ｇ悊] 鍗曡繛鎺ヤ笅琛岄檺閫? %.1f Mbps", downLimitMbps)
	}
	if idleTimeout > 0 {
		log.Printf("[浠ｇ悊] 闅ч亾绌洪棽瓒呮椂: %s", idleTimeout)
	}
	if serverIP != "" {
		log.Printf("[浠ｇ悊] 浣跨敤鍥哄畾 IP: %s", serverIP)
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("[浠ｇ悊] 鎺ュ彈杩炴帴澶辫触: %v", err)
			continue
		}

		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()

	clientAddr := conn.RemoteAddr().String()

	// 鍚敤 TCP_NODELAY 闄嶄綆寤惰繜锛屽瑙嗛娴佺瓑浜や簰寮忔祦閲忛噸瑕?	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetReadBuffer(256 * 1024)
		tcpConn.SetWriteBuffer(256 * 1024)
	}

	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// 璇诲彇绗竴涓瓧鑺傚垽鏂崗璁?	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return
	}

	firstByte := buf[0]

	// 浣跨敤 switch 鍒ゆ柇鍗忚绫诲瀷
	switch firstByte {
	case 0x05:
		// SOCKS5 鍗忚
		handleSOCKS5(conn, clientAddr, firstByte)
	case 'C', 'G', 'P', 'H', 'D', 'O', 'T':
		// HTTP 鍗忚 (CONNECT, GET, POST, HEAD, DELETE, OPTIONS, TRACE, PUT, PATCH)
		handleHTTP(conn, clientAddr, firstByte)
	default:
		log.Printf("[浠ｇ悊] %s 鏈煡鍗忚: 0x%02x", clientAddr, firstByte)
	}
}

// ======================== SOCKS5 澶勭悊 ========================

func handleSOCKS5(conn net.Conn, clientAddr string, firstByte byte) {
	// 楠岃瘉鐗堟湰
	if firstByte != 0x05 {
		log.Printf("[SOCKS5] %s 鐗堟湰閿欒: 0x%02x", clientAddr, firstByte)
		return
	}

	// 璇诲彇璁よ瘉鏂规硶鏁伴噺
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}

	nmethods := buf[0]
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	// 鍝嶅簲鏃犻渶璁よ瘉
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// 璇诲彇璇锋眰
	buf = make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}

	if buf[0] != 5 {
		return
	}

	command := buf[1]
	atyp := buf[3]

	var host string
	switch atyp {
	case 0x01: // IPv4
		buf = make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		host = net.IP(buf).String()

	case 0x03: // 鍩熷悕
		buf = make([]byte, 1)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		domainBuf := make([]byte, buf[0])
		if _, err := io.ReadFull(conn, domainBuf); err != nil {
			return
		}
		host = string(domainBuf)

	case 0x04: // IPv6
		buf = make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		host = net.IP(buf).String()

	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
		return
	}

	// 璇诲彇绔彛
	buf = make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	port := int(buf[0])<<8 | int(buf[1])

	switch command {
	case 0x01: // CONNECT
		var target string
		if atyp == 0x04 {
			target = fmt.Sprintf("[%s]:%d", host, port)
		} else {
			target = fmt.Sprintf("%s:%d", host, port)
		}

		log.Printf("[SOCKS5] %s -> %s", clientAddr, target)

		if err := handleTunnel(conn, target, clientAddr, modeSOCKS5, ""); err != nil {
			if !isNormalCloseError(err) {
				log.Printf("[SOCKS5] %s 浠ｇ悊澶辫触: %v", clientAddr, err)
			}
		}

	case 0x03: // UDP ASSOCIATE
		handleUDPAssociate(conn, clientAddr)

	default:
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
		return
	}
}

func handleUDPAssociate(tcpConn net.Conn, clientAddr string) {
	// 鍒涘缓 UDP 鐩戝惉鍣?	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		log.Printf("[UDP] %s 瑙ｆ瀽鍦板潃澶辫触: %v", clientAddr, err)
		tcpConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
		return
	}

	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Printf("[UDP] %s 鐩戝惉澶辫触: %v", clientAddr, err)
		tcpConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
		return
	}

	// 鑾峰彇瀹為檯鐩戝惉鐨勭鍙?	localAddr := udpConn.LocalAddr().(*net.UDPAddr)
	port := localAddr.Port

	log.Printf("[UDP] %s UDP ASSOCIATE 鐩戝惉绔彛: %d", clientAddr, port)

	// 鍙戦€佹垚鍔熷搷搴?	response := []byte{0x05, 0x00, 0x00, 0x01}
	response = append(response, 127, 0, 0, 1) // 127.0.0.1
	response = append(response, byte(port>>8), byte(port&0xff))

	if _, err := tcpConn.Write(response); err != nil {
		udpConn.Close()
		return
	}

	// 鍚姩 UDP 澶勭悊
	stopChan := make(chan struct{})
	go handleUDPRelay(udpConn, clientAddr, stopChan)

	// 淇濇寔 TCP 杩炴帴锛岀洿鍒板鎴风鍏抽棴
	buf := make([]byte, 1)
	tcpConn.Read(buf)

	close(stopChan)
	udpConn.Close()
	log.Printf("[UDP] %s UDP ASSOCIATE 杩炴帴鍏抽棴", clientAddr)
}

func handleUDPRelay(udpConn *net.UDPConn, clientAddr string, stopChan chan struct{}) {
	buf := make([]byte, 65535)
	for {
		select {
		case <-stopChan:
			return
		default:
		}

		udpConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		// 瑙ｆ瀽 SOCKS5 UDP 璇锋眰澶?		if n < 10 {
			continue
		}

		// SOCKS5 UDP 璇锋眰鏍煎紡:
		// +----+------+------+----------+----------+----------+
		// |RSV | FRAG | ATYP | DST.ADDR | DST.PORT |   DATA   |
		// +----+------+------+----------+----------+----------+
		// | 2  |  1   |  1   | Variable |    2     | Variable |
		// +----+------+------+----------+----------+----------+

		data := buf[:n]

		if data[2] != 0x00 { // FRAG 蹇呴』涓?0
			continue
		}

		atyp := data[3]
		var headerLen int
		var dstHost string
		var dstPort int

		switch atyp {
		case 0x01: // IPv4
			if n < 10 {
				continue
			}
			dstHost = net.IP(data[4:8]).String()
			dstPort = int(data[8])<<8 | int(data[9])
			headerLen = 10

		case 0x03: // 鍩熷悕
			if n < 5 {
				continue
			}
			domainLen := int(data[4])
			if n < 7+domainLen {
				continue
			}
			dstHost = string(data[5 : 5+domainLen])
			dstPort = int(data[5+domainLen])<<8 | int(data[6+domainLen])
			headerLen = 7 + domainLen

		case 0x04: // IPv6
			if n < 22 {
				continue
			}
			dstHost = net.IP(data[4:20]).String()
			dstPort = int(data[20])<<8 | int(data[21])
			headerLen = 22

		default:
			continue
		}

		udpData := data[headerLen:]
		target := fmt.Sprintf("%s:%d", dstHost, dstPort)

		// 妫€鏌ユ槸鍚︽槸 DNS 鏌ヨ锛堢鍙?53锛?		if dstPort == 53 {
			log.Printf("[UDP-DNS] %s -> %s (DoH 鏌ヨ)", clientAddr, target)
			go handleDNSQuery(udpConn, addr, udpData, data[:headerLen])
		} else {
			log.Printf("[UDP] %s -> %s (鏆備笉鏀寔闈?DNS UDP)", clientAddr, target)
			// 杩欓噷鍙互鎵╁睍鏀寔鍏朵粬 UDP 娴侀噺
		}
	}
}

func handleDNSQuery(udpConn *net.UDPConn, clientAddr *net.UDPAddr, dnsQuery []byte, socks5Header []byte) {
	// 閫氳繃 DoH 鏌ヨ锛堜娇鐢ㄩ噸鍛藉悕鍚庣殑鍑芥暟锛?	dnsResponse, err := queryDoHForProxy(dnsQuery)
	if err != nil {
		log.Printf("[UDP-DNS] DoH 鏌ヨ澶辫触: %v", err)
		return
	}

	// 鏋勫缓 SOCKS5 UDP 鍝嶅簲
	response := make([]byte, 0, len(socks5Header)+len(dnsResponse))
	response = append(response, socks5Header...)
	response = append(response, dnsResponse...)

	// 鍙戦€佸搷搴?	_, err = udpConn.WriteToUDP(response, clientAddr)
	if err != nil {
		log.Printf("[UDP-DNS] 鍙戦€佸搷搴斿け璐? %v", err)
		return
	}

	log.Printf("[UDP-DNS] DoH 鏌ヨ鎴愬姛锛屽搷搴?%d 瀛楄妭", len(dnsResponse))
}

// ======================== HTTP 澶勭悊 ========================

func handleHTTP(conn net.Conn, clientAddr string, firstByte byte) {
	// 灏嗙涓€涓瓧鑺傛斁鍥炵紦鍐插尯
	reader := bufio.NewReader(io.MultiReader(
		strings.NewReader(string(firstByte)),
		conn,
	))

	// 璇诲彇 HTTP 璇锋眰琛?	requestLine, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	parts := strings.Fields(requestLine)
	if len(parts) < 3 {
		return
	}

	method := parts[0]
	requestURL := parts[1]
	httpVersion := parts[2]

	// 璇诲彇鎵€鏈?headers
	headers := make(map[string]string)
	var headerLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		headerLines = append(headerLines, line)
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])
			headers[strings.ToLower(key)] = value
		}
	}

	switch method {
	case "CONNECT":
		// HTTPS 闅ч亾浠ｇ悊 - 闇€瑕佸彂閫?200 鍝嶅簲
		log.Printf("[HTTP-CONNECT] %s -> %s", clientAddr, requestURL)
		if err := handleTunnel(conn, requestURL, clientAddr, modeHTTPConnect, ""); err != nil {
			if !isNormalCloseError(err) {
				log.Printf("[HTTP-CONNECT] %s 浠ｇ悊澶辫触: %v", clientAddr, err)
			}
		}

	case "GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS", "PATCH", "TRACE":
		// HTTP 浠ｇ悊 - 鐩存帴杞彂锛屼笉鍙戦€?200 鍝嶅簲
		log.Printf("[HTTP-%s] %s -> %s", method, clientAddr, requestURL)

		var target string
		var path string

		if strings.HasPrefix(requestURL, "http://") {
			// 瑙ｆ瀽瀹屾暣 URL
			urlWithoutScheme := strings.TrimPrefix(requestURL, "http://")
			idx := strings.Index(urlWithoutScheme, "/")
			if idx > 0 {
				target = urlWithoutScheme[:idx]
				path = urlWithoutScheme[idx:]
			} else {
				target = urlWithoutScheme
				path = "/"
			}
		} else {
			// 鐩稿璺緞锛屼粠 Host header 鑾峰彇
			target = headers["host"]
			path = requestURL
		}

		if target == "" {
			conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			return
		}

		// 娣诲姞榛樿绔彛
		if !strings.Contains(target, ":") {
			target += ":80"
		}

		// 閲嶆瀯 HTTP 璇锋眰锛堝幓鎺夊畬鏁?URL锛屼娇鐢ㄧ浉瀵硅矾寰勶級
		var requestBuilder strings.Builder
		requestBuilder.WriteString(fmt.Sprintf("%s %s %s\r\n", method, path, httpVersion))

		// 鍐欏叆 headers锛堣繃婊ゆ帀 Proxy-Connection锛?		for _, line := range headerLines {
			key := strings.Split(line, ":")[0]
			keyLower := strings.ToLower(strings.TrimSpace(key))
			if keyLower != "proxy-connection" && keyLower != "proxy-authorization" {
				requestBuilder.WriteString(line)
				requestBuilder.WriteString("\r\n")
			}
		}
		requestBuilder.WriteString("\r\n")

		// 濡傛灉鏈夎姹備綋锛岄渶瑕佽鍙栧苟闄勫姞
		if contentLength := headers["content-length"]; contentLength != "" {
			var length int
			fmt.Sscanf(contentLength, "%d", &length)
			if length > 0 && length < 10*1024*1024 { // 闄愬埗 10MB
				body := make([]byte, length)
				if _, err := io.ReadFull(reader, body); err == nil {
					requestBuilder.Write(body)
				}
			}
		}

		firstFrame := requestBuilder.String()

		// 浣跨敤 modeHTTPProxy 妯″紡锛堜笉鍙戦€?200 鍝嶅簲锛?		if err := handleTunnel(conn, target, clientAddr, modeHTTPProxy, firstFrame); err != nil {
			if !isNormalCloseError(err) {
				log.Printf("[HTTP-%s] %s 浠ｇ悊澶辫触: %v", method, clientAddr, err)
			}
		}

	default:
		log.Printf("[HTTP] %s 涓嶆敮鎸佺殑鏂规硶: %s", clientAddr, method)
		conn.Write([]byte("HTTP/1.1 405 Method Not Allowed\r\n\r\n"))
	}
}

// ======================== 閫氱敤闅ч亾澶勭悊 ========================

// 浠ｇ悊妯″紡甯搁噺
const (
	modeSOCKS5      = 1 // SOCKS5 浠ｇ悊
	modeHTTPConnect = 2 // HTTP CONNECT 闅ч亾
	modeHTTPProxy   = 3 // HTTP 鏅€氫唬鐞嗭紙GET/POST绛夛級
)

// writeQueueSize 涓嬭寰呭啓闃熷垪闀垮害锛堜互娑堟伅涓哄崟浣嶏紝姣忔潯鏈€澶х害 64KB锛?// 鍙敤鏉ュ惛鏀剁煭鏃堕棿鐨勫啓闃诲锛岃繃澶т細鐧界櫧鍗犵敤鏈湴鍐呭瓨骞剁牬鍧?TCP 鑳屽帇鐨勫強鏃舵€?const writeQueueSize = 192

// tokenBucket 鍗曡繛鎺ヤ笅琛岄檺閫熷櫒锛堜护鐗屾《锛?type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	rate   float64 // bytes/s
	burst  float64 // bytes
	last   time.Time
}

// newTokenBucket 鎸?Mbps 鍒涘缓闄愰€熷櫒锛宮bps <= 0 鏃惰繑鍥?nil锛堣〃绀轰笉闄愰€燂級
func newTokenBucket(mbps float64) *tokenBucket {
	if mbps <= 0 {
		return nil
	}
	rate := mbps * 125000 // Mbps -> bytes/s
	burst := rate * 0.5   // 鍏佽 0.5 绉掔殑绐佸彂锛岄伩鍏嶉檺閫熸妸棣栧抚缂撳啿鎷栨參
	if burst < 65536 {
		burst = 65536
	}
	return &tokenBucket{tokens: burst, rate: rate, burst: burst, last: time.Now()}
}

// wait 闃诲鐩村埌鍑戝 n 瀛楄妭鐨勯厤棰?func (tb *tokenBucket) wait(n int) {
	if tb == nil || tb.rate <= 0 {
		return
	}
	for {
		tb.mu.Lock()
		now := time.Now()
		if d := now.Sub(tb.last).Seconds(); d > 0 {
			tb.tokens += tb.rate * d
			if tb.tokens > tb.burst {
				tb.tokens = tb.burst
			}
			tb.last = now
		}
		if tb.tokens >= float64(n) {
			tb.tokens -= float64(n)
			tb.mu.Unlock()
			return
		}
		deficit := float64(n) - tb.tokens
		sleep := time.Duration(deficit / tb.rate * float64(time.Second))
		tb.mu.Unlock()
		if sleep <= 0 {
			sleep = time.Millisecond
		}
		time.Sleep(sleep)
	}
}

func handleTunnel(conn net.Conn, target, clientAddr string, mode int, firstFrame string) error {
	// 瑙ｆ瀽鐩爣鍦板潃
	targetHost, _, err := net.SplitHostPort(target)
	if err != nil {
		targetHost = target
	}

	// 妫€鏌ユ槸鍚﹀簲璇ョ粫杩囦唬鐞嗭紙鐩磋繛锛?	rule := "proxy"
	if shouldBypassProxy(targetHost) {
		rule = "direct"
		log.Printf("[鍒嗘祦] %s -> %s (鐩磋繛, 妯″紡=%s)", clientAddr, target, routingMode)
		return handleDirectConnection(conn, target, clientAddr, mode, firstFrame)
	}

	// 璧颁唬鐞?	modeStr := "socks5"
	if mode == modeHTTPConnect {
		modeStr = "http-connect"
	} else if mode == modeHTTPProxy {
		modeStr = "http-proxy"
	}
	connID := addConn(clientAddr, target, modeStr, rule)
	connInfoObj, _ := activeConns.Load(connID)
	connInfoPtr := connInfoObj.(*connInfo)

	log.Printf("[鍒嗘祦] %s -> %s (閫氳繃浠ｇ悊, 妯″紡=%s)", clientAddr, target, routingMode)
	wsConn, err := dialWebSocketWithECH(2)
	if err != nil {
		removeConn(connID)
		sendErrorResponse(conn, mode)
		return err
	}

	var mu sync.Mutex
	var closed atomic.Bool
	stopCh := make(chan struct{}) // cleanup 鏃跺箍鎾紝鐢ㄤ簬鍞ら啋琚?channel 闃诲鐨?goroutine
	closeOnce := sync.Once{}
	cleanup := func() {
		closeOnce.Do(func() {
			closed.Store(true)
			select {
			case <-stopCh:
			default:
				close(stopCh)
			}
			wsConn.Close()
			conn.Close()
		})
	}
	defer cleanup()

	// ========== 淇濇椿涓庢椿鎬у垽瀹?==========
	// 鍒绘剰涓嶄娇鐢?gorilla 鐨?SetReadDeadline 浣滀负闀胯繛鎺ョ殑璇昏秴鏃讹細
	// 瑙嗛鎾斁鍣ㄧ紦鍐插～婊″悗浼氬仠姝㈣鍙栨暟鎹紝姝ゆ椂 Server->Client 浼氶樆濉炲湪 conn.Write 涓婏紝
	// 浜庢槸鍐嶄篃娌′汉璋冪敤 ReadMessage锛宺ead deadline 鍒版湡灏变細鎶婁竴鏉′粛鍦ㄦ甯镐娇鐢ㄧ殑闅ч亾璇潃锛?	// 鑰?ping/pong 涔熸晳涓嶄簡锛坧ong 鍙湪 ReadMessage 璋冪敤鏈熼棿鎵嶄細琚鐞嗭級銆?	// 鏀圭敤缁熶竴鐨勭┖闂茶鏃跺櫒锛氫换涓€鏂瑰悜鏈夋暟鎹祦鍔ㄥ氨缁湡锛岀湡姝ｆ寔缁┖闂叉墠鍒ゅ畾涓烘杩炴帴銆?	var lastActive atomic.Int64 // UnixNano
	touch := func() { lastActive.Store(time.Now().UnixNano()) }
	touch()

	wsConn.SetPongHandler(func(string) error {
		touch()
		return nil
	})

	stopPing := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mu.Lock()
				if !closed.Load() {
					wsConn.SetWriteDeadline(time.Now().Add(15 * time.Second))
					wsConn.WriteMessage(websocket.PingMessage, nil)
					wsConn.SetWriteDeadline(time.Time{})
				}
				mu.Unlock()
			case <-stopPing:
				return
			}
		}
	}()
	defer close(stopPing)

	// 绌洪棽瓒呮椂宸℃锛氭瘮 read deadline 鏇村噯纭湴琛ㄨ揪"杩欐潯杩炴帴宸茬粡娌′汉鐢ㄤ簡"
	stopIdle := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if idleTimeout <= 0 {
					continue
				}
				last := lastActive.Load()
				if last > 0 && time.Since(time.Unix(0, last)) > idleTimeout && !closed.Load() {
					log.Printf("[浠ｇ悊] %s 绌洪棽瓒呰繃 %s锛屽叧闂毀閬?, clientAddr, idleTimeout)
					cleanup()
					return
				}
			case <-stopIdle:
				return
			}
		}
	}()
	defer close(stopIdle)

	conn.SetDeadline(time.Time{})

	// 濡傛灉娌℃湁棰勮鐨?firstFrame锛屽皾璇曡鍙栫涓€甯ф暟鎹紙浠?SOCKS5锛?	if firstFrame == "" && mode == modeSOCKS5 {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buffer := make([]byte, 65536)
		n, _ := conn.Read(buffer)
		_ = conn.SetReadDeadline(time.Time{})
		if n > 0 {
			firstFrame = string(buffer[:n])
		}
	}

	// 鍙戦€佽繛鎺ヨ姹?	connectMsg := fmt.Sprintf("CONNECT:%s|%s", target, firstFrame)
	mu.Lock()
	err = wsConn.WriteMessage(websocket.TextMessage, []byte(connectMsg))
	mu.Unlock()
	if err != nil {
		removeConn(connID)
		sendErrorResponse(conn, mode)
		return err
	}

	// 绛夊緟鍝嶅簲锛堟彙鎵嬮樁娈典粛淇濈暀璇昏秴鏃讹紝閬垮厤杩炰笉涓婃椂姘镐箙鎸備綇锛?	wsConn.SetReadDeadline(time.Now().Add(20 * time.Second))
	_, msg, err := wsConn.ReadMessage()
	if err != nil {
		removeConn(connID)
		sendErrorResponse(conn, mode)
		return err
	}
	// 鎻℃墜瀹屾垚鍚庡彇娑堝簳灞傝瓒呮椂锛岄暱杩炴帴鐨勬椿鎬ф敼鐢变笂闈㈢殑绌洪棽璁℃椂鍣ㄨ礋璐?	wsConn.SetReadDeadline(time.Time{})
	touch()

	response := string(msg)
	if strings.HasPrefix(response, "ERROR:") {
		removeConn(connID)
		sendErrorResponse(conn, mode)
		return errors.New(response)
	}
	if response != "CONNECTED" {
		removeConn(connID)
		sendErrorResponse(conn, mode)
		return fmt.Errorf("鎰忓鍝嶅簲: %s", response)
	}

	// 鍙戦€佹垚鍔熷搷搴旓紙鏍规嵁妯″紡涓嶅悓鑰屼笉鍚岋級
	if err := sendSuccessResponse(conn, mode); err != nil {
		removeConn(connID)
		return err
	}

	log.Printf("[浠ｇ悊] %s 宸茶繛鎺? %s", clientAddr, target)

	// 鍙屽悜杞彂
	// done 蹇呴』鏄甫缂撳啿鐨勶紝涓斿彂閫佹柟鐢ㄩ樆濉炲彂閫侊細
	// 涓嬮潰鎭板ソ鏈?2 涓崗绋嬪悇鑷彂閫佷竴娆★紙Client->Server銆佹帓绌?writeQueue 鐨勫啓鍗忕▼锛夛紝
	// 缂撳啿 2 淇濊瘉涓ゆ鍙戦€侀兘涓嶄細闃诲锛涘彧瑕佹湁涓€涓崗绋嬬粨鏉燂紝涓诲崗绋嬪氨鑳界珛鍒绘敹灏俱€?	// 鍒囧嬁鏀瑰洖 select/default 鈥斺€?閭ｇ瓑浜庡厑璁镐俊鍙疯涓㈠純锛屼袱涓崗绋嬭嫢閮藉厛浜庝富鍗忕▼閫€鍑猴紝
	// 涓诲崗绋嬩細姘镐箙闃诲鍦?<-done 涓婏紝cleanup 姘镐笉鎵ц锛岃繛鎺ヤ笌 goroutine 鍏ㄩ儴娉勬紡銆?	done := make(chan struct{}, 2)

	// Client -> Server
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if !isNormalCloseError(err) {
					log.Printf("[浠ｇ悊] %s 瀹㈡埛绔鍙栭敊璇? %v", clientAddr, err)
				}
				mu.Lock()
				if !closed.Load() {
					wsConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
					wsConn.WriteMessage(websocket.TextMessage, []byte("CLOSE"))
					wsConn.SetWriteDeadline(time.Time{})
				}
				mu.Unlock()
				return
			}

			connInfoPtr.upload.Add(int64(n))
			touch()

			mu.Lock()
			wsConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			err = wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
			wsConn.SetWriteDeadline(time.Time{})
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	// Server -> Client
	// 杩欓噷鎶娿€岃 WebSocket銆嶅拰銆屽啓缁欐湰鍦拌繛鎺ャ€嶆媶鍒颁袱涓?goroutine锛屼腑闂寸敤涓€涓湁鐣岄槦鍒楄鎺ャ€?	// 鍘熷洜锛氭挱鏀惧櫒缂撳啿濉弧鍚庡仠姝㈣鍙栨椂锛宑onn.Write 浼氶暱鏃堕棿闃诲銆傝嫢璇诲啓鍦ㄥ悓涓€ goroutine锛?	// 闃诲鏈熼棿灏辨病浜哄幓璇?WebSocket锛屼笂灞傞摼璺畬鍏ㄥ仠鎽嗭紝涓旈敊杩?pong 澶勭悊銆?	// 闃熷垪鏈夌晫锛堣 writeQueueSize锛夛紝鍐欎笉杩涘幓鏃惰渚т篃浼氶樆濉烇紝TCP 鑳屽帇浠嶇劧鑳戒紶瀵煎埌婧愮珯銆?	writeQueue := make(chan []byte, writeQueueSize)

	go func() {
		// 娉ㄦ剰锛氳繖閲屼笉涓诲姩閫氱煡 done銆傝渚х粨鏉熸椂鍙叧闂槦鍒楋紝鐢卞啓渚ф妸闃熷垪鎺掔┖鍚庡啀鏀跺熬锛?		// 鍚﹀垯鏈嶅姟绔彂瀹屾暟鎹氨鍏?WS锛圚TTP 鍝嶅簲缁撴潫鐨勫父瑙佸舰鎬侊級鏃讹紝灏鹃儴鏁版嵁浼氳鎴帀銆?		defer close(writeQueue)
		for {
			mt, msg, err := wsConn.ReadMessage()
			if err != nil {
				if !isNormalCloseError(err) {
					log.Printf("[浠ｇ悊] %s WebSocket 璇诲彇閿欒: %v", clientAddr, err)
				}
				return
			}

			touch()

			if mt == websocket.TextMessage {
				if string(msg) == "CLOSE" {
					return
				}
			}

			connInfoPtr.download.Add(int64(len(msg)))

			// gorilla 鐨?ReadMessage 姣忔杩斿洖鐙珛鍒嗛厤鐨勫垏鐗囷紝鍙畨鍏ㄤ氦缁欏彟涓€涓?goroutine
			select {
			case writeQueue <- msg:
			case <-stopCh:
				return
			}
		}
	}()

	// 涓嬭闄愰€熷櫒锛氭寜鍗曡繛鎺ュ钩婊戣緭鍑猴紝閬垮厤鐭椂闂村唴鎶婂ぇ閲忔暟鎹帹鍚戞挱鏀惧櫒锛?	// 浠庤€屽湪 worker 渚х殑 send 闃熷垪閲屽爢绉紙Cloudflare 姣忔潯 WS 鍗曠嫭闄愯祫婧愶紝鍫嗙Н鍚?GC 鍘嬪姏浼氳鍚炲悙鎸佺画涓嬮檷锛?	limiter := newTokenBucket(downLimitMbps)

	go func() {
		defer func() { done <- struct{}{} }()
		for msg := range writeQueue {
			limiter.wait(len(msg))
			written := 0
			for written < len(msg) {
				n, err := conn.Write(msg[written:])
				if err != nil {
					return
				}
				written += n
			}
		}
	}()

	<-done
	cleanup()
	removeConn(connID)
	log.Printf("[浠ｇ悊] %s 宸叉柇寮€: %s", clientAddr, target)
	return nil
}

// ======================== 鐩磋繛澶勭悊 ========================

// handleDirectConnection 澶勭悊鐩磋繛锛堢粫杩囦唬鐞嗭級
func handleDirectConnection(conn net.Conn, target, clientAddr string, mode int, firstFrame string) error {
	// 瑙ｆ瀽鐩爣鍦板潃
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		// 濡傛灉娌℃湁绔彛锛屾牴鎹ā寮忔坊鍔犻粯璁ょ鍙?		host = target
		if mode == modeHTTPConnect || mode == modeHTTPProxy {
			port = "443"
		} else {
			port = "80"
		}
		target = net.JoinHostPort(host, port)
	}

	modeStr := "socks5"
	if mode == modeHTTPConnect {
		modeStr = "http-connect"
	} else if mode == modeHTTPProxy {
		modeStr = "http-proxy"
	}
	connID := addConn(clientAddr, target, modeStr, "direct")
	connInfoObj, _ := activeConns.Load(connID)
	connInfoPtr := connInfoObj.(*connInfo)

	// 鐩存帴杩炴帴鍒扮洰鏍?	targetConn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		removeConn(connID)
		sendErrorResponse(conn, mode)
		return fmt.Errorf("鐩磋繛澶辫触: %w", err)
	}

	// 瀵圭洿杩炵洰鏍囦篃鍚敤 TCP_NODELAY
	if tcpConn, ok := targetConn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetReadBuffer(256 * 1024)
		tcpConn.SetWriteBuffer(256 * 1024)
	}

	closeOnce := sync.Once{}
	cleanup := func() {
		closeOnce.Do(func() {
			targetConn.Close()
			conn.Close()
		})
	}
	defer cleanup()

	conn.SetDeadline(time.Time{})

	// 鍙戦€佹垚鍔熷搷搴?	if err := sendSuccessResponse(conn, mode); err != nil {
		removeConn(connID)
		return err
	}

	// 濡傛灉鏈夐璁剧殑绗竴甯ф暟鎹紝鍏堝彂閫?	if firstFrame != "" {
		data := []byte(firstFrame)
		written := 0
		for written < len(data) {
			n, err := targetConn.Write(data[written:])
			if err != nil {
				removeConn(connID)
				return err
			}
			written += n
		}
	}

	// 鍙屽悜杞彂
	// done 甯︾紦鍐?+ 闃诲鍙戦€侊紝鐞嗙敱鍚?handleTunnel锛氫笅闈㈡伆濂?2 涓彂閫佹柟锛岀紦鍐?2 淇濊瘉涓嶄細闃诲銆?	done := make(chan struct{}, 2)

	// Client -> Target
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			connInfoPtr.upload.Add(int64(n))
			written := 0
			for written < n {
				m, err := targetConn.Write(buf[written:n])
				if err != nil {
					return
				}
				written += m
			}
		}
	}()

	// Target -> Client
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 65536)
		for {
			n, err := targetConn.Read(buf)
			if err != nil {
				return
			}
			connInfoPtr.download.Add(int64(n))
			written := 0
			for written < n {
				m, err := conn.Write(buf[written:n])
				if err != nil {
					return
				}
				written += m
			}
		}
	}()

	<-done
	cleanup()
	removeConn(connID)
	log.Printf("[鍒嗘祦] %s 鐩磋繛宸叉柇寮€: %s", clientAddr, target)
	return nil
}

// ======================== 鍝嶅簲杈呭姪鍑芥暟 ========================

func sendErrorResponse(conn net.Conn, mode int) {
	switch mode {
	case modeSOCKS5:
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	case modeHTTPConnect, modeHTTPProxy:
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
	}
}

func sendSuccessResponse(conn net.Conn, mode int) error {
	switch mode {
	case modeSOCKS5:
		// SOCKS5 鎴愬姛鍝嶅簲
		_, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
		return err
	case modeHTTPConnect:
		// HTTP CONNECT 闇€瑕佸彂閫?200 鍝嶅簲
		_, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		return err
	case modeHTTPProxy:
		// HTTP GET/POST 绛変笉闇€瑕佸彂閫佸搷搴旓紝鐩存帴杞彂鐩爣鏈嶅姟鍣ㄧ殑鍝嶅簲
		return nil
	}
	return nil
}
