package main

// 每日定时自重启：到达 -restart-at 时间后进程内 re-exec（Unix）或退化为退出进程，
// 用于清理长时间运行后的状态劣化，不依赖容器 restart 策略。

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

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
