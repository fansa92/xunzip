package main

import (
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	dsnetbzip2 "github.com/dsnet/compress/bzip2"
	pgzip "github.com/klauspost/compress/gzip"
)

// newBzip2WriterForTar 创建 bzip2 压缩器（供 tar 重写使用）。
func newBzip2WriterForTar(w io.Writer, opt options) (*dsnetbzip2.Writer, error) {
	return dsnetbzip2.NewWriter(w, &dsnetbzip2.WriterConfig{Level: levelOrDefault(opt, 6)})
}

// parseSize 解析 "32m" / "64k" / "1g" / "1048576" 这类大小字符串，返回字节数。
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("空的大小值")
	}
	mul := int64(1)
	switch s[len(s)-1] {
	case 'b', 'B':
		s = s[:len(s)-1]
	case 'k', 'K':
		mul, s = 1<<10, s[:len(s)-1]
	case 'm', 'M':
		mul, s = 1<<20, s[:len(s)-1]
	case 'g', 'G':
		mul, s = 1<<30, s[:len(s)-1]
	}
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("无效的大小 %q", s)
	}
	return v * mul, nil
}

// levelOrDefault 返回用户指定的压缩级别，未指定时用 def。
func levelOrDefault(opt options, def int) int {
	if opt.level < 0 {
		return def
	}
	return opt.level
}

// newGzipWriter 创建 gzip 压缩器。threads>1 时用 klauspost 的并行实现
// （已在依赖树中，默认分块并发压缩），否则用标准库 compress/gzip。
func newGzipWriter(w io.Writer, opt options) (io.WriteCloser, error) {
	level := levelOrDefault(opt, gzip.DefaultCompression)
	if opt.threads > 1 {
		return pgzip.NewWriterLevel(w, level)
	}
	return gzip.NewWriterLevel(w, level)
}

// newXzWriter 创建 xz 压缩器；-dict 通过 DictCap 指定字典大小。
func newXzWriter(w io.Writer, opt options) (*xz.Writer, error) {
	cfg := xz.WriterConfig{}
	if opt.dict != "" {
		n, err := parseSize(opt.dict)
		if err != nil {
			return nil, err
		}
		if n > 1<<31-1 {
			return nil, fmt.Errorf("-dict 过大")
		}
		cfg.DictCap = int(n)
	}
	return cfg.NewWriter(w)
}

// newZstdWriter 创建 zstd 压缩器；级别 0..9 映射到 zstd 官方 level，
// -threads 控制并发编码。
func newZstdWriter(w io.Writer, opt options) (*zstd.Encoder, error) {
	level := zstd.SpeedDefault
	if opt.level >= 0 {
		level = zstd.EncoderLevelFromZstd(opt.level)
	}
	opts := []zstd.EOption{zstd.WithEncoderLevel(level)}
	if opt.threads > 0 {
		opts = append(opts, zstd.WithEncoderConcurrency(opt.threads))
	}
	return zstd.NewWriter(w, opts...)
}

// zipMethod 根据 -level / -method 决定 zip 条目的压缩方法。
func zipMethod(opt options) uint16 {
	switch strings.ToLower(opt.method) {
	case "store", "copy", "0":
		return zip.Store
	case "deflate", "":
		return zip.Deflate
	}
	return zip.Deflate
}

// flateLevel 把 -level 转成 flate 级别；zip 通过 RegisterCompressor 使用。
func flateLevel(opt options) int {
	if opt.level < 0 {
		return gzip.DefaultCompression
	}
	return opt.level
}
