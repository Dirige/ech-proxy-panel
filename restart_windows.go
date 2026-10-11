//go:build windows

package main

import "errors"

// selfRestart Windows 不支持进程内镜像替换，返回错误让调用方退化为退出进程。
func selfRestart(exe string) error {
	return errors.New("Windows 不支持 re-exec 自重启")
}
