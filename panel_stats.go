package main

// 连接追踪、流量统计、日志缓冲，以及原版 handleTunnel 中 [面板钩子 C/D] 的实现。

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

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
