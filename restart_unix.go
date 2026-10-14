//go:build !windows

package main

import (
	"os"
	"syscall"
)

// selfRestart 用当前二进制与参数原地替换进程镜像（re-exec）。
// Linux/Docker 下同 PID、同容器、不依赖任何 restart 策略，
// 旧状态（连接、goroutine、内存）全部清零，等效「手动重启成功」。
func selfRestart(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
