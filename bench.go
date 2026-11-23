package main

import (
	"fmt"
	"os"
	"time"
)

// runBench 对指定压缩包做"读取解压"基准（不落盘）：测吞吐与耗时。
func runBench(p parsedCLI) int {
	if len(p.args) == 0 {
		fmt.Fprintln(os.Stderr, "错误: b 命令需要指定一个压缩包")
		return 7
	}
	arc := p.args[0]
	if err := ensureArchiveExists(arc); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		return 2
	}
	f := resolveFormat(arc, p.opt)
	secs := p.opt.benchSec
	if secs < 1 {
		secs = 5
	}

	fmt.Printf("基准测试: %s [%s]，约 %d 秒\n", arc, formatName(f), secs)
	var totalBytes int64
	var rounds int
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		st, err := benchOnce(arc, f, p.opt)
		if err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			return 2
		}
		totalBytes += st.bytes
		rounds++
	}
	elapsed := time.Since(deadline) + time.Duration(secs)*time.Second
	if elapsed <= 0 {
		elapsed = time.Second
	}
	rate := float64(totalBytes) / elapsed.Seconds() / (1 << 20)
	fmt.Printf("完成 %d 轮，解压 %d 字节，吞吐 %.2f MiB/s（%.2f 秒）\n",
		rounds, totalBytes, rate, elapsed.Seconds())
	return 0
}

func benchOnce(arc string, f format, opt options) (stats, error) {
	opt.quiet = true
	opt.testOnly = true // 走 integrity 测试路径，不落盘
	return processArchive(arc, opt)
}
