//go:build !windows

package main

// consoleCP 在非 Windows 平台上不需要特殊处理。
type consoleCP struct{}

func setupConsoleUTF8() *consoleCP { return &consoleCP{} }

func (c *consoleCP) restore() {}
