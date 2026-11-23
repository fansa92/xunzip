package main

import (
	"fmt"
	"io"
	"os"
)

// stdinToTemp 把 stdin 读到一个临时文件，返回路径与清理函数。
// 用于 -si：让 stdin 参与原本面向文件路径的处理流程。
func stdinToTemp() (string, func(), error) {
	f, err := os.CreateTemp("", "xunzip-stdin-*")
	if err != nil {
		return "", nil, err
	}
	if _, err := io.Copy(f, os.Stdin); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// outputToWriter 返回应向其写入解压结果的目标（stdout 或文件）。
func outputToWriter(dst string, opt options) (io.Writer, func() error, error) {
	if opt.stdout {
		return os.Stdout, func() error { return nil }, nil
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// decompressToWriter 把单文件流解压后写到指定 writer（stdout 或文件）。
func decompressToWriter(src string, w io.Writer, wrap wrapFn) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	r, cleanup, err := wrap(in)
	if err != nil {
		return 0, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	return io.Copy(w, r)
}

// describePipe 生成 -so 时的诊断信息（写 stderr，避免污染 stdout）。
func describePipe(format, target string) {
	fmt.Fprintf(os.Stderr, "输出到 stdout [%s]\n", format)
}
