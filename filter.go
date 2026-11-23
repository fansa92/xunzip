package main

import (
	"path/filepath"
	"strings"
)

// shouldExtract 根据 -i/-x 模式决定是否处理该条目。
// 规则：无模式则全部通过；有 -i 时需命中任一；命中 -x 时排除。
func shouldExtract(name string, opt options) bool {
	n := filepath.ToSlash(name)
	for _, pat := range opt.exclude {
		if matchAny(pat, n) {
			return false
		}
	}
	if len(opt.include) == 0 {
		return true
	}
	for _, pat := range opt.include {
		if matchAny(pat, n) {
			return true
		}
	}
	return false
}

// matchAny 支持 * 通配符（匹配任意字符含路径分隔）与精确匹配。
func matchAny(pattern, name string) bool {
	pat := filepath.ToSlash(strings.TrimSpace(pattern))
	if pat == "" {
		return false
	}
	if !strings.Contains(pat, "*") && !strings.Contains(pat, "?") {
		return pat == name || pat == filepath.Base(name)
	}
	// 文件名匹配。
	if ok, _ := filepath.Match(pat, filepath.Base(name)); ok {
		return true
	}
	// 全路径匹配（* 不跨 / 时用 filepath.Match，跨 / 用简化 glob）。
	if ok, _ := filepath.Match(pat, name); ok {
		return true
	}
	return globMatch(pat, name)
}

// globMatch 简化的 ** 跨目录匹配。
func globMatch(pattern, name string) bool {
	parts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")
	pi, ni := 0, 0
	for pi < len(parts) && ni < len(nameParts) {
		if parts[pi] == "**" {
			pi++
			if pi >= len(parts) {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(parts[pi], nameParts[ni]); !ok {
			return false
		}
		pi++
		ni++
	}
	return pi == len(parts) && ni == len(nameParts)
}
