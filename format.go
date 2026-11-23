package main

import (
	"fmt"
	"os"
	"strings"
)

// resolveFormat 决定文件使用的格式：-type 优先，否则按后缀 + 魔数核对。
func resolveFormat(path string, opt options) format {
	if opt.forceType != "" {
		if f := formatFromName(opt.forceType); f != fmtUnknown {
			return f
		}
	}
	f := detectFormat(path)
	if f != fmtUnknown {
		if sf := sniffFormat(path); sf != fmtUnknown && sf != f {
			return sf
		}
		return f
	}
	return sniffFormat(path)
}

// formatFromName 把格式名字符串转成 format。
func formatFromName(name string) format {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), ".")) {
	case "zip":
		return fmtZip
	case "tar":
		return fmtTar
	case "tar.gz", "tgz", "gzip":
		return fmtTarGz
	case "tar.bz2", "tbz2", "tbz":
		return fmtTarBz2
	case "tar.xz", "txz", "xz":
		return fmtTarXz
	case "tar.zst", "tzst", "zstd":
		return fmtTarZst
	case "gz":
		return fmtGz
	case "bz2", "bzip2":
		return fmtBz2
	case "zst":
		return fmtZst
	case "rar":
		return fmtRar
	case "7z":
		return fmtSevenZ
	case "lz4":
		return fmtLZ4
	case "br", "brotli":
		return fmtBrotli
	case "lz", "lzip":
		return fmtLZ
	case "lzma":
		return fmtLZMA
	}
	return fmtUnknown
}

// formatInfo 用于 i 命令。
type formatInfo struct {
	name  string
	read  bool
	write bool
	via   string
}

func allFormatInfo() []formatInfo {
	return []formatInfo{
		{"zip", true, true, "标准库 + alexmullins/zip(AES) + yeka/zip(ZipCrypto)"},
		{"rar / rar5", true, false, "nwaples/rardecode/v2（只读）"},
		{"7z", true, true, "解压 bodgit/sevenzip / 写入 go-filesystems/sevenzip（Store+LZMA2）"},
		{"tar", true, true, "标准库 archive/tar"},
		{"tar.gz / tgz", true, true, "标准库 gzip（多线程用 klauspost/compress/gzip）"},
		{"tar.bz2 / tbz2", true, true, "解压标准库 / 压缩 dsnet/compress"},
		{"tar.xz / txz", true, true, "ulikunitz/xz"},
		{"tar.zst / tzst", true, true, "klauspost/compress/zstd"},
		{"gz", true, true, "标准库"},
		{"bz2", true, true, "解压标准库 / 压缩 dsnet"},
		{"xz", true, true, "ulikunitz/xz"},
		{"zst", true, true, "klauspost/compress/zstd"},
		{"lz4", true, true, "pierrec/lz4/v4"},
		{"br", true, true, "andybalholm/brotli"},
		{"lz", true, true, "自拼 lzip 头 + ulikunitz/xz/lzma"},
		{"lzma", true, true, "ulikunitz/xz/lzma"},
	}
}

func runInfo(p parsedCLI) int {
	fmt.Println("xunzip 支持的格式：")
	fmt.Printf("%-16s %-6s %-6s %s\n", "格式", "解压", "压缩", "实现")
	for _, fi := range allFormatInfo() {
		r, w := "是", "否"
		if fi.read {
			r = "是"
		} else {
			r = "-"
		}
		if fi.write {
			w = "是"
		} else {
			w = "-"
		}
		fmt.Printf("%-16s %-6s %-6s %s\n", fi.name, r, w, fi.via)
	}
	fmt.Println("\n说明：rar 为只读格式（7-Zip 本身也不创建 rar）；7z 可读写，但写入只支持")
	fmt.Println("Store/LZMA2、不支持加密与 solid 块、不保留修改时间与符号链接类型。")
	fmt.Println("加密包（zip/rar/7z）解压用 -p；创建加密 zip 用 -p。")
	fmt.Println("内置 Zip-Slip 防护、symlink 安全校验、四态覆盖模式(-ao)。")
	return 0
}

// ensureArchiveExists 检查压缩包是否存在且非目录。
func ensureArchiveExists(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("无法访问 %q: %w", path, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q 是目录，不是压缩包", path)
	}
	return nil
}
