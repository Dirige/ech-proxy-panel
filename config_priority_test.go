package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

// resetCfgState 把全局参数与来源标记恢复到"刚启动、什么都没设置"的状态
func resetCfgState() {
	cliSet = map[string]bool{}
	envSet = map[string]string{}
	fromConfig = map[string]bool{}
	listenAddr = "0.0.0.0:30000"
	serverAddr = "hhech.nb1tap.kdns.fr:443"
	serverIP = ""
	token = "honghongfree"
	dnsServer = "dns.alidns.com/dns-query"
	echDomain = "cloudflare-ech.com"
	routingMode = "bypass_cn"
	webAddr = ""
	rulesFile = ""
	rulesData = "/data/rules.json"
	configFile = "/data/config.json"
	proxyIP = ""
	webPassword = ""
	downLimitMbps = 0
	idleTimeout = 15 * time.Minute
}

// 1) 没有任何命令行/环境变量时，config.json 完全生效
func TestConfigFileAppliesWhenNothingElseSet(t *testing.T) {
	resetCfgState()
	applyConfigFile(&appConfig{
		ListenAddr:     "127.0.0.1:19098",
		ServerAddr:     "node.example.com:443",
		Token:          "tok-from-file",
		RoutingMode:    "global",
		WebAddr:        ":19099",
		WebPassword:    "pw-from-file",
		DownLimitMbps:  f64(33.5),
		IdleTimeoutMin: f64(7),
	})

	if serverAddr != "node.example.com:443" || token != "tok-from-file" ||
		routingMode != "global" || webAddr != ":19099" || webPassword != "pw-from-file" {
		t.Fatalf("config.json 未生效: server=%q token=%q routing=%q web=%q pw=%q",
			serverAddr, token, routingMode, webAddr, webPassword)
	}
	if downLimitMbps != 33.5 {
		t.Fatalf("down_limit_mbps 未生效: %v", downLimitMbps)
	}
	if idleTimeout != 7*time.Minute {
		t.Fatalf("idle_timeout_min 未生效: %v", idleTimeout)
	}
	if got := fieldSource("f", "server_addr"); got != "config.json" {
		t.Fatalf("来源应为 config.json，实际 %q", got)
	}
}

// 2) 回归用例：环境变量的取值恰好等于内置默认值时，也必须压过 config.json
//    （旧实现用"值 == 默认值"判断是否已设置，这里会错误地让 config.json 覆盖 env）
func TestEnvWinsEvenWhenValueEqualsBuiltinDefault(t *testing.T) {
	resetCfgState()
	// 完全复刻用户的 compose
	t.Setenv("ECH_SERVER", "hhech.nb1tap.kdns.fr:443") // == 内置默认
	t.Setenv("ECH_TOKEN", "honghongfree")              // == 内置默认
	t.Setenv("ECH_ROUTING", "bypass_cn")               // == 内置默认
	t.Setenv("ECH_WEB", ":9090")                       // == 内置默认
	t.Setenv("ECH_PASSWORD", "liu1752256014")

	applyEnvDefaults()
	if envSet["server_addr"] != "ECH_SERVER" {
		t.Fatalf("ECH_SERVER 应被记录为已设置，envSet=%v", envSet)
	}

	// config.json 里放的是另一套值
	applyConfigFile(&appConfig{
		ServerAddr:  "old-node.example.com:443",
		Token:       "old-token",
		RoutingMode: "global",
		WebAddr:     ":18080",
		WebPassword: "old-password",
	})

	if serverAddr != "hhech.nb1tap.kdns.fr:443" {
		t.Fatalf("环境变量未压过 config.json: serverAddr=%q", serverAddr)
	}
	if token != "honghongfree" {
		t.Fatalf("环境变量未压过 config.json: token=%q", token)
	}
	if routingMode != "bypass_cn" {
		t.Fatalf("环境变量未压过 config.json: routingMode=%q", routingMode)
	}
	if webPassword != "liu1752256014" {
		t.Fatalf("环境变量未压过 config.json: webPassword=%q", webPassword)
	}
	for _, c := range []struct{ flag, key string }{
		{"f", "server_addr"}, {"token", "token"}, {"routing", "routing_mode"}, {"password", "web_password"},
	} {
		if got := fieldSource(c.flag, c.key); got != "环境变量" {
			t.Fatalf("%s 来源应为 环境变量，实际 %q", c.key, got)
		}
	}
}

// 3) 命令行显式给出时优先级最高
func TestCLIWinsOverEnvAndConfig(t *testing.T) {
	resetCfgState()
	t.Setenv("ECH_SERVER", "env-node.example.com:443")
	applyEnvDefaults()
	cliSet["f"] = true
	serverAddr = "cli-node.example.com:443"

	applyConfigFile(&appConfig{ServerAddr: "cfg-node.example.com:443"})
	if serverAddr != "cli-node.example.com:443" {
		t.Fatalf("命令行未获胜: %q", serverAddr)
	}
	if got := fieldSource("f", "server_addr"); got != "命令行" {
		t.Fatalf("来源应为 命令行，实际 %q", got)
	}
}

// 4) 显式设置的 0 不能被环境变量或 config.json 顶掉
func TestExplicitZeroSurvives(t *testing.T) {
	resetCfgState()
	cliSet["downlimit"] = true
	cliSet["idle-timeout"] = true
	downLimitMbps = 0
	idleTimeout = 0

	t.Setenv("ECH_DOWN_LIMIT", "50")
	t.Setenv("ECH_IDLE_TIMEOUT", "30m")
	applyEnvDefaults()
	applyConfigFile(&appConfig{DownLimitMbps: f64(88), IdleTimeoutMin: f64(99)})

	if downLimitMbps != 0 {
		t.Fatalf("显式的 0 被覆盖为 %v", downLimitMbps)
	}
	if idleTimeout != 0 {
		t.Fatalf("显式的 0 被覆盖为 %v", idleTimeout)
	}
}

// 5) config.json 里写 0 也要生效（指针区分"没有这个键"与"显式 0"）
func TestConfigZeroApplies(t *testing.T) {
	resetCfgState()
	applyConfigFile(&appConfig{DownLimitMbps: f64(0), IdleTimeoutMin: f64(0)})
	if downLimitMbps != 0 || idleTimeout != 0 {
		t.Fatalf("config.json 的 0 未生效: %v / %v", downLimitMbps, idleTimeout)
	}
	// 键不存在（nil）时应保持内置默认
	resetCfgState()
	applyConfigFile(&appConfig{})
	if idleTimeout != 15*time.Minute {
		t.Fatalf("键不存在时不应改动默认值: %v", idleTimeout)
	}
	if got := fieldSource("idle-timeout", "idle_timeout_min"); got != "内置默认" {
		t.Fatalf("来源应为 内置默认，实际 %q", got)
	}
}

// 6) 面板规则文件：空文件=真正清空；损坏文件=保留现有规则（不误删）
func TestLoadRulesDataFileBehaviour(t *testing.T) {
	resetCfgState()
	dir := t.TempDir()
	rulesData = filepath.Join(dir, "rules.json")

	// 损坏内容：保留已有规则
	customRulesMu.Lock()
	customRules = []customRule{{Type: "domain", Value: "keep.me", Action: "direct"}}
	customRulesMu.Unlock()
	if err := os.WriteFile(rulesData, []byte("这不是合法规则\n随便写点东西\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadRulesDataFile()
	customRulesMu.RLock()
	n := len(customRules)
	customRulesMu.RUnlock()
	if n != 1 {
		t.Fatalf("损坏文件不应清掉现有规则，实际 %d 条", n)
	}

	// 合法内容：正常加载
	if err := os.WriteFile(rulesData, []byte("domain,a.example,direct\ndomain,b.example,proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadRulesDataFile()
	customRulesMu.RLock()
	n = len(customRules)
	customRulesMu.RUnlock()
	if n != 2 {
		t.Fatalf("应加载 2 条规则，实际 %d 条", n)
	}

	// 空白内容：面板主动清空，规则应被清掉
	if err := os.WriteFile(rulesData, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadRulesDataFile()
	customRulesMu.RLock()
	n = len(customRules)
	customRulesMu.RUnlock()
	if n != 0 {
		t.Fatalf("空白规则文件应清空规则，实际 %d 条", n)
	}

	// 文件不存在：保持现状（不清空）
	customRulesMu.Lock()
	customRules = []customRule{{Type: "keyword", Value: "abc", Action: "proxy"}}
	customRulesMu.Unlock()
	os.Remove(rulesData)
	loadRulesDataFile()
	customRulesMu.RLock()
	n = len(customRules)
	customRulesMu.RUnlock()
	if n != 1 {
		t.Fatalf("规则文件不存在时不应改动现有规则，实际 %d 条", n)
	}
}

// 7) 规则文件保存后能原样读回（原子替换不破坏内容）
func TestSaveAndReloadRules(t *testing.T) {
	resetCfgState()
	dir := t.TempDir()
	rulesData = filepath.Join(dir, "sub", "rules.json") // 目录不存在也要能创建
	customRulesMu.Lock()
	customRules = []customRule{
		{Type: "domain", Value: "*.example.com", Action: "proxy"},
		{Type: "ipcidr", Value: "10.0.0.0/8", Action: "direct"},
	}
	customRulesMu.Unlock()

	if err := saveCustomRules(rulesData); err != nil {
		t.Fatalf("保存规则失败: %v", err)
	}
	customRulesMu.Lock()
	customRules = nil
	customRulesMu.Unlock()

	loadRulesDataFile()
	customRulesMu.RLock()
	defer customRulesMu.RUnlock()
	if len(customRules) != 2 || customRules[0].Value != "*.example.com" || customRules[1].Action != "direct" {
		t.Fatalf("规则未能原样读回: %+v", customRules)
	}
}
