//go:build windows

package main

import "syscall"

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
)

const utf8CP = 65001

// consoleCP 记录控制台原来的代码页，用于退出时恢复。
type consoleCP struct{ old uintptr }

// setupConsoleUTF8 把控制台输出代码页临时切到 UTF-8，
// 否则中文在 GBK 控制台上会显示成乱码。
func setupConsoleUTF8() *consoleCP {
	old, _, _ := procGetConsoleOutputCP.Call()
	if old != 0 && old != utf8CP {
		procSetConsoleOutputCP.Call(utf8CP)
	}
	return &consoleCP{old: old}
}

// restore 恢复原来的代码页。
func (c *consoleCP) restore() {
	if c != nil && c.old != 0 && c.old != utf8CP {
		procSetConsoleOutputCP.Call(c.old)
	}
}
