package main

import (
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz/lzma"
)

// ---- 解压包装 ----

func wrapLZ4(r io.Reader) (io.Reader, func(), error) {
	return lz4.NewReader(r), nil, nil
}

func wrapBrotli(r io.Reader) (io.Reader, func(), error) {
	return brotli.NewReader(r), nil, nil
}

// wrapLZ 处理 lzip：跳过 6 字节头（LZIP + version + dict code），
// 余下是标准 LZMA_Alone 流。
func wrapLZ(r io.Reader) (io.Reader, func(), error) {
	var hdr [6]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, nil, fmt.Errorf("lzip 头不完整: %w", err)
	}
	if string(hdr[:4]) != "LZIP" {
		return nil, nil, fmt.Errorf("不是有效的 lzip 文件")
	}
	lr, err := lzma.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return lr, nil, nil
}

func wrapLZMA(r io.Reader) (io.Reader, func(), error) {
	lr, err := lzma.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return lr, nil, nil
}

// ---- 压缩写入 ----

func newLZ4Writer(w io.Writer) (io.WriteCloser, error) {
	lw := lz4.NewWriter(w)
	return lw, nil
}

func newBrotliWriter(w io.Writer, opt options) (io.WriteCloser, error) {
	level := 6 // brotli 默认质量
	if opt.level >= 0 {
		q := opt.level * 11 / 9
		if q > 11 {
			q = 11
		}
		level = q
	}
	return brotli.NewWriterOptions(w, brotli.WriterOptions{Quality: level}), nil
}

// newLZIPWriter 写 lzip 头（版本 1，字典 8MiB=code 0x0c），后接 LZMA_Alone 流。
func newLZIPWriter(w io.Writer) (io.WriteCloser, error) {
	dictCode := byte(0x0c) // 8 MiB
	if _, err := w.Write([]byte{'L', 'Z', 'I', 'P', 1, dictCode}); err != nil {
		return nil, err
	}
	lw, err := lzma.NewWriter(w)
	if err != nil {
		return nil, err
	}
	return lw, nil
}

func newLZMAWriter(w io.Writer) (io.WriteCloser, error) {
	return lzma.NewWriter(w)
}

// ---- 单文件名的后缀推导 ----

// isExtraSingle 报告是否为新加入的单文件流格式。
func isExtraSingle(f format) bool {
	switch f {
	case fmtLZ4, fmtBrotli, fmtLZ, fmtLZMA:
		return true
	}
	return false
}

// singleWrapper 返回单文件流格式的解压 wrapper（gz/bz2/xz/zst + 新格式）。
func singleWrapper(f format) wrapFn {
	switch f {
	case fmtGz:
		return wrapGzip
	case fmtBz2:
		return wrapBzip2
	case fmtXz:
		return wrapXz
	case fmtZst:
		return wrapZstd
	case fmtLZ4:
		return wrapLZ4
	case fmtBrotli:
		return wrapBrotli
	case fmtLZ:
		return wrapLZ
	case fmtLZMA:
		return wrapLZMA
	}
	return nil
}

func stripExtraSuffix(base string) string {
	for _, suf := range []string{".lz4", ".br", ".lz", ".lzip", ".lzma"} {
		if len(base) > len(suf) && base[len(base)-len(suf):] == suf {
			return base[:len(base)-len(suf)]
		}
	}
	return base
}
