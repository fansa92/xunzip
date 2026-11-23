package main

import (
	"os"
	"runtime"
)

// applyMOTW 给解压出的文件写入 Zone.Identifier ADS，标记来自网络。
// 仅 Windows 有效；其它平台为 no-op。失败时静默忽略，不影响解压结果。
func applyMOTW(path string, opt options) {
	if !opt.motw || runtime.GOOS != "windows" {
		return
	}
	ads := path + ":Zone.Identifier"
	_ = os.WriteFile(ads, []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
}
