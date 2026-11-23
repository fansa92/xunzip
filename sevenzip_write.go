package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-filesystems/sevenzip"
)

// 7z 写入：基于 github.com/go-filesystems/sevenzip（纯 Go，无 cgo）。
//
// 该库只实现两种存储方法和够用的头部子集，能力边界因此是明确的：
//   - 方法只有 Store 与 LZMA2，没有 PPMd / BZip2 / Deflate / Delta / BCJ 过滤器；
//   - 不支持加密（没有 AES-256 + 加密头）、不支持 solid 块、不记录修改时间；
//   - 属性字只有 S_IFREG / S_IFDIR，没有 S_IFLNK，符号链接只能按普通文件存。
// 所以下面每个不支持项都给出明确提示，而不是静默降级成"看起来成功"。

// sevenZipLevelDict 把 -level 0..9 近似映射到 LZMA2 字典大小。
// 数值参照 7-Zip 的默认表；字典大小在格式里是固定的几个档位，
// 库会把请求值向上取到能表示的那一档，所以这里不必精确命中。
func sevenZipLevelDict(level int) int {
	switch level {
	case 1:
		return 64 << 10 // 64 KB
	case 2:
		return 1 << 20 // 1 MB
	case 3:
		return 4 << 20 // 4 MB
	case 4:
		return 8 << 20 // 8 MB
	case 5:
		return 16 << 20 // 16 MB
	case 6:
		return 32 << 20 // 32 MB
	case 7:
		return 64 << 20 // 64 MB
	case 8:
		return 128 << 20 // 128 MB
	default:
		return 256 << 20 // 9 及以上
	}
}

// sevenZipMethod 决定 7z 条目的存储方法。
// 默认 LZMA2（7-Zip 自己的默认）；-level 0 或 -method store/copy 走不压缩。
func sevenZipMethod(opt options) sevenzip.Method {
	switch strings.ToLower(strings.TrimSpace(opt.method)) {
	case "store", "copy", "none":
		return sevenzip.Store
	case "lzma2", "lzma", "deflate", "":
		// deflate 在 7z 里不存在，退到 LZMA2 而不是报错。
	default:
		// 已由 validate7zOptions 拦住，这里只是兜底。
		return sevenzip.LZMA2
	}
	if opt.level == 0 {
		return sevenzip.Store
	}
	return sevenzip.LZMA2
}

// sevenZipDict 返回请求的字典大小，0 表示用库默认值。
func sevenZipDict(opt options) (int, error) {
	if d := strings.TrimSpace(opt.dict); d != "" {
		n, err := parseSize(d)
		if err != nil {
			return 0, fmt.Errorf("-dict 无效: %w", err)
		}
		if n > 1<<31-1 {
			return 0, fmt.Errorf("-dict 过大（%s），LZMA2 字典上限 2 GiB 且需 64 位寻址", d)
		}
		return int(n), nil
	}
	if opt.level <= 0 {
		// 未指定（-1）用 16 MB，对应 7-Zip 的 normal 档；0 是 Store，不需要字典。
		if opt.level == 0 {
			return 0, nil
		}
		return 16 << 20, nil
	}
	return sevenZipLevelDict(opt.level), nil
}

// validate7zOptions 在动手写之前把 7z 表达不了的选项拒掉或提示。
func validate7zOptions(opt options) error {
	if opt.password != "" {
		return fmt.Errorf("7z 写入不支持加密：纯 Go 写入库只实现 Store/LZMA2，没有 AES-256 与加密头；请改用 .zip -p 加密，或先用 7-Zip 创建加密包")
	}
	if opt.stdout {
		return fmt.Errorf("7z 写入不能直写 stdout：档案头要回填开头 32 字节，需要可寻址输出（io.WriteSeeker）")
	}
	if m := strings.ToLower(strings.TrimSpace(opt.method)); m != "" {
		switch m {
		case "store", "copy", "none", "lzma2", "lzma", "deflate":
		default:
			return fmt.Errorf("7z 只支持 -method store 或 lzma2（收到 %q）", opt.method)
		}
	}
	return nil
}

// write7z 把条目打包成 .7z。
func write7z(outPath string, entries []arcEntry, opt options) (stats, error) {
	var st stats
	if err := validate7zOptions(opt); err != nil {
		return st, err
	}
	if !opt.quiet {
		if opt.threads > 1 {
			fmt.Fprintln(os.Stderr, "注意: 7z 写入是单线程编码，-threads 对 .7z 不生效")
		}
		if opt.solid {
			fmt.Fprintln(os.Stderr, "注意: 纯 Go 7z 写入不生成 solid 块，每个条目独立编码（解压时无法整体流式读取）")
		}
	}

	method := sevenZipMethod(opt)
	out, err := openCreate(outPath, opt)
	if err != nil {
		return st, err
	}
	zopts := []sevenzip.Option{sevenzip.WithMethod(method)}
	dict, err := sevenZipDict(opt)
	if err != nil {
		out.Close()
		removePartial(outPath)
		return st, err
	}
	if method == sevenzip.LZMA2 && dict > 0 {
		zopts = append(zopts, sevenzip.WithDictionary(dict))
	}
	zw, err := sevenzip.NewWriter(out, zopts...)
	if err != nil {
		out.Close()
		removePartial(outPath)
		return st, fmt.Errorf("初始化 7z 写入失败: %w", err)
	}
	fail := func(err error) (stats, error) {
		out.Close()
		removePartial(outPath)
		return stats{}, err
	}

	for _, e := range entries {
		switch {
		case e.isDir:
			// AddDir 不写流，权限进 kWinAttributes 的高 16 位。
			if err := zw.AddDir(e.arcPath, dirPerm(e.info.Mode().Perm())); err != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
			}
			st.dirs++
		case e.isLink:
			// 写入库的属性字只有 S_IFREG/S_IFDIR：链接类型存不下来，
			// 只能退化成"内容是目标路径的普通文件"。
			if !opt.quiet {
				fmt.Fprintf(os.Stderr, "注意: 7z 不保留符号链接，%q 会作为普通文件存为目标路径 %q\n", e.arcPath, e.link)
			}
			if err := zw.AddFile(e.arcPath, filePerm(e.info.Mode().Perm()), 0, strings.NewReader(e.link)); err != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
			}
			st.files++
			st.bytes += int64(len(e.link))
		default:
			in, err := os.Open(e.fsPath)
			if err != nil {
				return fail(fmt.Errorf("读取失败 %q: %w", e.fsPath, err))
			}
			// AddFile 忽略 size（7z 的长度记在末尾的头里，写入库自己数），
			// 这里传真实大小只是为了语义清楚。
			addErr := zw.AddFile(e.arcPath, filePerm(e.info.Mode().Perm()), e.info.Size(), in)
			in.Close()
			if addErr != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, addErr))
			}
			st.files++
			st.bytes += e.info.Size()
		}
		if !opt.quiet {
			fmt.Printf("a %s\n", e.arcPath)
		}
	}

	if err := zw.Close(); err != nil {
		out.Close()
		removePartial(outPath)
		return stats{}, fmt.Errorf("写入 7z 头失败: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		removePartial(outPath)
		return stats{}, err
	}
	if err := out.Close(); err != nil {
		removePartial(outPath)
		return stats{}, err
	}
	return st, nil
}
