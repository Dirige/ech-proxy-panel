package main

// 分流规则（direct/proxy 两桶）、DNS 解析缓存与分流决策。
// 规则在所有分流模式下优先于模式生效；未命中规则时回落原版 shouldBypassProxy。

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

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
