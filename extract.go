package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	yekazip "github.com/yeka/zip" // archive/zip 的加密 fork：支持 WinZip AES 与传统 ZipCrypto
)

// format 是支持的压缩包类型。
type format int

const (
	fmtUnknown format = iota
	fmtZip
	fmtTar
	fmtTarGz
	fmtTarBz2
	fmtTarXz
	fmtTarZst
	fmtGz  // 单文件 gzip（非 tar）
	fmtBz2 // 单文件 bzip2（非 tar）
	fmtXz  // 单文件 xz（非 tar）
	fmtZst // 单文件 zstd（非 tar）
	fmtRar
	fmtSevenZ
	fmtLZ4    // 单文件 lz4 帧（.lz4）
	fmtBrotli // 单文件 brotli（.br）
	fmtLZ     // 单文件 lzip（.lz）
	fmtLZMA   // 单文件 LZMA_Alone（.lzma）
)

// stats 记录一次解压的结果。
type stats struct {
	files int
	dirs  int
	bytes int64
	skip  int // 因已存在而跳过的文件数
}

// detectFormat 按文件后缀判断格式（大小写不敏感）。
// 注意 .tar.gz / .tgz 等必须先匹配长后缀。
func detectFormat(path string) format {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return fmtZip
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return fmtTarGz
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"),
		strings.HasSuffix(lower, ".tbz"), strings.HasSuffix(lower, ".tar.bzip2"):
		return fmtTarBz2
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		return fmtTarXz
	case strings.HasSuffix(lower, ".tar.zst"), strings.HasSuffix(lower, ".tzst"):
		return fmtTarZst
	case strings.HasSuffix(lower, ".tar"):
		return fmtTar
	case strings.HasSuffix(lower, ".rar"):
		return fmtRar
	case strings.HasSuffix(lower, ".7z"):
		return fmtSevenZ
	case strings.HasSuffix(lower, ".gz"):
		return fmtGz
	case strings.HasSuffix(lower, ".xz"):
		return fmtXz
	case strings.HasSuffix(lower, ".zst"):
		return fmtZst
	case strings.HasSuffix(lower, ".bz2"), strings.HasSuffix(lower, ".bzip2"):
		return fmtBz2
	case strings.HasSuffix(lower, ".lz4"):
		return fmtLZ4
	case strings.HasSuffix(lower, ".br"):
		return fmtBrotli
	case strings.HasSuffix(lower, ".lz"), strings.HasSuffix(lower, ".lzip"):
		return fmtLZ
	case strings.HasSuffix(lower, ".lzma"):
		return fmtLZMA
	default:
		return fmtUnknown
	}
}

// formatName 返回格式的可读名称。
func formatName(f format) string {
	switch f {
	case fmtZip:
		return "zip"
	case fmtTar:
		return "tar"
	case fmtTarGz:
		return "tar.gz"
	case fmtTarBz2:
		return "tar.bz2"
	case fmtTarXz:
		return "tar.xz"
	case fmtTarZst:
		return "tar.zst"
	case fmtGz:
		return "gzip"
	case fmtXz:
		return "xz"
	case fmtZst:
		return "zstd"
	case fmtBz2:
		return "bzip2"
	case fmtRar:
		return "rar"
	case fmtSevenZ:
		return "7z"
	case fmtLZ4:
		return "lz4"
	case fmtBrotli:
		return "brotli"
	case fmtLZ:
		return "lzip"
	case fmtLZMA:
		return "lzma"
	default:
		return "未知"
	}
}

// isContainer 报告该格式是否为容器（含多文件），还是单文件流。
func isContainer(f format) bool {
	switch f {
	case fmtZip, fmtTar, fmtTarGz, fmtTarBz2, fmtTarXz, fmtTarZst, fmtRar, fmtSevenZ:
		return true
	default:
		return false
	}
}

// stripArchiveSuffix 去掉压缩包后缀，用于推导单文件解压后的文件名。
// 如 "a.zip" -> "a"，"b.tar.gz" -> "b"，"c.txt.gz" -> "c.txt"。
func stripArchiveSuffix(base string) string {
	lower := strings.ToLower(base)
	for _, suf := range []string{".tar.gz", ".tar.bz2", ".tar.bzip2", ".tar.xz", ".tar.zst", ".tbz2", ".tbz", ".tgz", ".txz", ".tzst"} {
		if strings.HasSuffix(lower, suf) {
			return base[:len(base)-len(suf)]
		}
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[:i]
	}
	return base + "_out"
}

// ---- 魔数嗅探 ----

// sniffFormat 读文件头魔数判断格式，无法识别返回 fmtUnknown。
// 对 gz / bz2 / xz / zst 会解开开头一小段判断里面是不是 tar，
// 从而把 tar.gz 之类的复合格式也直接识别出来。
func sniffFormat(path string) format {
	in, err := os.Open(path)
	if err != nil {
		return fmtUnknown
	}
	defer in.Close()
	var buf [16]byte
	n, _ := io.ReadFull(in, buf[:])
	head := buf[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")),
		bytes.HasPrefix(head, []byte("PK\x05\x06")), // 空 zip
		bytes.HasPrefix(head, []byte("PK\x07\x08")): // 分卷 zip
		return fmtZip
	case bytes.HasPrefix(head, []byte("Rar!\x1a\x07")): // RAR4 / RAR5
		return fmtRar
	case bytes.HasPrefix(head, []byte{0x37, 0x7a, 0xbc, 0xaf, 0x27, 0x1c}): // 7z
		return fmtSevenZ
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}): // gzip
		if peekIsTar(path, wrapGzip) {
			return fmtTarGz
		}
		return fmtGz
	case bytes.HasPrefix(head, []byte("BZh")): // bzip2
		if peekIsTar(path, wrapBzip2) {
			return fmtTarBz2
		}
		return fmtBz2
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}): // xz
		if peekIsTar(path, wrapXz) {
			return fmtTarXz
		}
		return fmtXz
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}): // zstd
		if peekIsTar(path, wrapZstd) {
			return fmtTarZst
		}
		return fmtZst
	case bytes.HasPrefix(head, []byte{0x04, 0x22, 0x4d, 0x18}): // lz4 frame
		return fmtLZ4
	case looksLikeBrotli(head):
		return fmtBrotli
	case bytes.HasPrefix(head, []byte("LZIP")): // lzip
		return fmtLZ
	case bytes.HasPrefix(head, []byte{0x5d, 0x00, 0x00, 0x80, 0x00}): // lzma_alone 常见头
		return fmtLZMA
	case looksLikeTarHead(path):
		return fmtTar
	}
	return fmtUnknown
}

// looksLikeBrotli 粗略判断 brotli 流：首字节高位为非 0 的窗口位标记。
// 作为魔数嗅探的弱信号，仅在后缀无法识别时使用，避免误判。
func looksLikeBrotli(head []byte) bool {
	if len(head) < 2 {
		return false
	}
	// Brotli 流首字节的高 4 位通常是窗口大小（0x8/0x9/0xA/0xB... 起始的 WBITS）。
	b := head[0]
	return b == 0x8b || b == 0x9b || b == 0xab || b == 0xbb || b == 0xcb || b == 0xdb || b == 0xeb || b == 0xfb
}

// wrapFn 把原始流包成解压流；返回的 cleanup 在读完后调用（可为 nil 包装）。
type wrapFn func(io.Reader) (io.Reader, func(), error)

func wrapGzip(r io.Reader) (io.Reader, func(), error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return gr, func() { _ = gr.Close() }, nil
}

func wrapBzip2(r io.Reader) (io.Reader, func(), error) {
	return bzip2.NewReader(r), nil, nil
}

func wrapXz(r io.Reader) (io.Reader, func(), error) {
	xr, err := xz.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return xr, nil, nil
}

func wrapZstd(r io.Reader) (io.Reader, func(), error) {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return nil, nil, err
	}
	return zr, zr.Close, nil
}

// peekIsTar 重新打开文件并解压开头一段，判断内层是否为 tar。
func peekIsTar(path string, wrap wrapFn) bool {
	in, err := os.Open(path)
	if err != nil {
		return false
	}
	defer in.Close()
	r, cleanup, err := wrap(in)
	if err != nil {
		return false
	}
	if cleanup != nil {
		defer cleanup()
	}
	var blk [512]byte
	if n, err := io.ReadFull(r, blk[:]); err != nil || n < 512 {
		return false
	}
	return looksLikeTarBlock(blk[:])
}

// looksLikeTarHead 判断未压缩文件的开头 512 字节是否为 tar 头。
func looksLikeTarHead(path string) bool {
	in, err := os.Open(path)
	if err != nil {
		return false
	}
	defer in.Close()
	var blk [512]byte
	if n, err := io.ReadFull(in, blk[:]); err != nil || n < 512 {
		return false
	}
	return looksLikeTarBlock(blk[:])
}

// looksLikeTarBlock 按 tar 头特征判断一个 512 字节块：
// POSIX/GNU 看 257 偏移处的 "ustar" 魔数；v7 tar 无魔数，用头部校验和验证。
func looksLikeTarBlock(b []byte) bool {
	if len(b) < 512 {
		return false
	}
	if bytes.HasPrefix(b[257:], []byte("ustar")) {
		return true
	}
	zero := make([]byte, 512)
	if bytes.Equal(b, zero) {
		return false
	}
	// v7：checksum 字段（148..156）本身按空格参与求和。
	sum := 0
	for _, c := range b[:148] {
		sum += int(c)
	}
	for i := 0; i < 8; i++ {
		sum += int(' ')
	}
	for _, c := range b[156:512] {
		sum += int(c)
	}
	chk, err := strconv.ParseInt(strings.Trim(string(b[148:156]), " \x00"), 8, 64)
	return err == nil && chk > 0 && int64(sum) == chk
}

// safeJoinOpt 根据选项计算 strip：flat(-e) 表示只保留最后一级文件名。
func safeJoinOpt(outDir, name string, opt options) (string, bool) {
	strip := opt.strip
	if opt.flat {
		// 平铺：把除最后一级外的所有路径都剥掉。
		n := strings.Count(strings.ReplaceAll(name, "\\", "/"), "/")
		if n > strip {
			strip = n
		}
	}
	return safeJoin(outDir, name, strip)
}

// safeJoin 把压缩包内的相对路径拼到输出目录下，并做 Zip-Slip 防护。
// 返回 ("", false) 表示路径非法（绝对路径 / 跳出目录），调用方应跳过。
func safeJoin(outDir, name string, strip int) (string, bool) {
	// 兼容 Windows 打包工具可能用的反斜杠。
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(name))
	clean = strings.TrimPrefix(clean, "./")
	if clean == "." || clean == "" {
		return "", false
	}
	// 跨平台拒绝绝对路径：filepath.IsAbs 在 Windows 下对 "/abs" 返回 false，
	// 所以再显式拦截 "/" 开头与 "C:/" 这类盘符路径。
	if filepath.IsAbs(clean) || filepath.IsAbs(name) ||
		strings.HasPrefix(clean, "/") ||
		(len(clean) >= 2 && clean[1] == ':' && ((clean[0] >= 'A' && clean[0] <= 'Z') || (clean[0] >= 'a' && clean[0] <= 'z'))) {
		return "", false
	}
	parts := strings.Split(clean, "/")
	// 去掉空段（处理 "a//b"）。
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	parts = kept
	if len(parts) == 0 {
		return "", false
	}
	// 拒绝任何 ".." 段；Clean 后 ".." 只会出现在开头，但逐段检查更稳。
	for _, p := range parts {
		if p == ".." {
			return "", false
		}
	}
	// --strip：去掉前 N 级目录。
	if strip > 0 {
		if len(parts) <= strip {
			return "", false // 被 strip 吃掉整条路径，跳过
		}
		parts = parts[strip:]
		clean = strings.Join(parts, "/")
	}
	target := filepath.Join(outDir, filepath.FromSlash(clean))
	rel, err := filepath.Rel(outDir, target)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

// processArchive 是单个压缩包的统一入口：测试、列出或解压。
func processArchive(archivePath string, opt options) (stats, error) {
	var st stats
	f := detectFormat(archivePath)
	// -type 强制指定格式：完全跳过检测。
	if opt.forceType != "" {
		if forced := formatFromName(opt.forceType); forced != fmtUnknown {
			f = forced
		}
	}
	if opt.forceType == "" {
		if f != fmtUnknown {
			// 后缀能识别时也核对一下魔数：内容与后缀不符则按实际内容处理。
			if sf := sniffFormat(archivePath); sf != fmtUnknown && sf != f {
				if !opt.quiet {
					fmt.Printf("注意: %q 后缀为 %s，但内容是 %s，按 %s 处理\n",
						archivePath, formatName(f), formatName(sf), formatName(sf))
				}
				f = sf
			}
		} else {
			f = sniffFormat(archivePath)
			if f == fmtUnknown {
				return st, fmt.Errorf("不支持的格式 %q（可用 -type 强制指定）", archivePath)
			}
			if !opt.quiet {
				fmt.Printf("注意: %q 无识别后缀，按 %s 处理\n", archivePath, formatName(f))
			}
		}
	}
	fi, err := os.Stat(archivePath)
	if err != nil {
		return st, fmt.Errorf("无法访问 %q: %w", archivePath, err)
	}
	if fi.IsDir() {
		return st, fmt.Errorf("%q 是一个目录，不是压缩包", archivePath)
	}

	if opt.testOnly {
		st, err = testArchive(archivePath, f, opt)
		if err != nil {
			return st, err
		}
		if !opt.quiet {
			fmt.Printf("测试通过: %s （%d 文件 / %d 目录 / %d 字节）\n", archivePath, st.files, st.dirs, st.bytes)
		}
		return st, nil
	}
	if opt.listOnly {
		return st, listArchive(archivePath, f, opt)
	}

	if isContainer(f) {
		outDir := strings.TrimSpace(opt.out)
		if outDir == "" {
			outDir = "." // 缺省解到当前目录
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return st, fmt.Errorf("创建输出目录失败: %w", err)
		}
		switch f {
		case fmtZip:
			st, err = extractZip(archivePath, outDir, opt)
		case fmtTar, fmtTarGz, fmtTarBz2, fmtTarXz, fmtTarZst:
			st, err = extractTar(archivePath, f, outDir, opt)
		case fmtRar:
			st, err = extractRar(archivePath, outDir, opt)
		case fmtSevenZ:
			st, err = extractSevenZ(archivePath, outDir, opt)
		default:
			err = fmt.Errorf("未知格式")
		}
		if err != nil {
			return st, err
		}
		if !opt.quiet {
			fmt.Printf("输出目录: %s （%d 文件 / %d 目录 / %d 字节", outDir, st.files, st.dirs, st.bytes)
			if st.skip > 0 {
				fmt.Printf(" / 跳过 %d", st.skip)
			}
			fmt.Println("）")
		}
		return st, nil
	}

	// 单文件流格式：缺省解到当前目录，-o 指定输出目录。
	base := stripArchiveSuffix(filepath.Base(archivePath))
	if isExtraSingle(f) {
		base = stripExtraSuffix(filepath.Base(archivePath))
	}
	outFile := ""
	if strings.TrimSpace(opt.out) == "" {
		outFile = filepath.Join(".", base)
	} else {
		outDir := strings.TrimSpace(opt.out)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return st, fmt.Errorf("创建输出目录失败: %w", err)
		}
		outFile = filepath.Join(outDir, base)
	}
	wrap := singleWrapper(f)
	if wrap == nil {
		return st, fmt.Errorf("未知的单文件格式")
	}
	if opt.stdout {
		if !opt.quiet {
			describePipe(formatName(f), outFile)
		}
		var n int64
		n, err = decompressToWriter(archivePath, os.Stdout, wrap)
		st.bytes = n
		st.files = 1
		if err != nil {
			return st, err
		}
		return st, nil
	}
	err = decompressSingleWrapped(archivePath, outFile, opt, wrap)
	if err != nil {
		return st, err
	}
	if !opt.quiet {
		fmt.Printf("已解压: %s -> %s\n", archivePath, outFile)
	}
	st.files = 1
	if fi2, e := os.Stat(outFile); e == nil {
		st.bytes = fi2.Size()
	}
	return st, nil
}

// errReader 把构造 reader 时的错误延迟到 Read 时返回。
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// decompressSingleWrapped 解压单文件流；wrapFn 携带可能失败的构造与 cleanup。
func decompressSingleWrapped(src, dst string, opt options, wrap wrapFn) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r, cleanup, err := wrap(in)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	return writeDecompressedStream(r, dst, opt)
}

// decompressSingle 解压单文件流（兼容旧签名，wrap 不返回错误）。
func decompressSingle(src, dst string, opt options, wrap func(io.Reader) io.Reader) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r := wrap(in)
	return writeDecompressedStream(r, dst, opt)
}

// writeDecompressedStream 把解压流写到目标文件，处理覆盖模式与失败清理。
func writeDecompressedStream(r io.Reader, dst string, opt options) error {

	// -ao 覆盖模式：解析目标路径（重命名/跳过/覆盖）。
	if opt.overwriteMode() != owAll {
		resolved, err := resolveTarget(dst, opt)
		if err != nil {
			return err
		}
		dst = resolved
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if opt.overwriteMode() == owSkip {
		flag |= os.O_EXCL
	}
	out, err := os.OpenFile(dst, flag, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("目标已存在 %q（用 -ao a 或 -f 覆盖）", dst)
		}
		return err
	}
	_, copyErr := io.CopyBuffer(out, r, make([]byte, 128*1024))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst) // 失败不留半截文件，下次不用 -f 也能重试
		return fmt.Errorf("解压失败: %w", copyErr)
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// hasAESExtra 报告 zip 条目的 extra field 里是否带 WinZip AES 字段（ID 0x9901）。
func hasAESExtra(extra []byte) bool {
	for i := 0; i+4 <= len(extra); {
		id := int(extra[i]) | int(extra[i+1])<<8
		n := int(extra[i+2]) | int(extra[i+3])<<8
		if id == 0x9901 {
			return true
		}
		i += 4 + n
	}
	return false
}

// zipEntry 抹平标准库 archive/zip 与 AES fork（aeszip）的条目接口，
// 解压与 -t 测试共用一套循环。
type zipEntry struct {
	name      string
	info      os.FileInfo
	modTime   time.Time
	encrypted bool
	hint      string // 加密相关错误的附加提示
	open      func() (io.ReadCloser, error)
}

// openZipEntries 打开 zip 并转为统一条目。普通包用标准库（保留扩展时间戳等
// 完整元数据）；发现加密条目（WinZip AES / ZipCrypto）时改用 AES fork，需要密码。
func openZipEntries(archivePath string, opt options) ([]*zipEntry, func(), error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 zip 失败: %w", err)
	}
	enc := false
	for _, f := range zr.File {
		if f.Flags&0x1 != 0 { // 通用标志位 bit0 = 已加密
			enc = true
			break
		}
	}
	if !enc {
		entries := make([]*zipEntry, 0, len(zr.File))
		for _, f := range zr.File {
			ff := f
			entries = append(entries, &zipEntry{
				name:    ff.Name,
				info:    ff.FileInfo(),
				modTime: ff.Modified,
				open:    func() (io.ReadCloser, error) { return ff.Open() },
			})
		}
		return entries, func() { _ = zr.Close() }, nil
	}
	// 加密包：标准库无法解密，切换到 yeka/zip（同时支持 WinZip AES 与传统 ZipCrypto）。
	zr.Close()
	if opt.password == "" {
		return nil, nil, fmt.Errorf("zip 含加密条目，请用 -p 指定密码")
	}
	azr, err := yekazip.OpenReader(archivePath)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 zip 失败: %w", err)
	}
	entries := make([]*zipEntry, 0, len(azr.File))
	for _, f := range azr.File {
		ff := f
		if ff.IsEncrypted() {
			ff.SetPassword(opt.password)
		}
		k := hasAESExtra(ff.Extra)
		entries = append(entries, &zipEntry{
			name:      ff.Name,
			info:      ff.FileInfo(),
			modTime:   ff.ModTime(),
			encrypted: ff.IsEncrypted(),
			hint:      "（解密失败：密码可能不正确，或数据已损坏）",
			open:      func() (io.ReadCloser, error) { return ff.Open() },
		})
		_ = k // AES 与 ZipCrypto 均由 fork 处理
	}
	return entries, func() { _ = azr.Close() }, nil
}

// extractZip 解压 zip（含目录、空目录、symlink 的 best-effort 处理）。
func extractZip(archivePath, outDir string, opt options) (stats, error) {
	var st stats
	entries, closeZip, err := openZipEntries(archivePath, opt)
	if err != nil {
		return st, err
	}
	defer closeZip()

	for _, f := range entries {
		if !shouldExtract(f.name, opt) {
			continue
		}
		target, ok := safeJoinOpt(outDir, f.name, opt)
		if !ok {
			if !opt.quiet {
				fmt.Fprintf(os.Stderr, "跳过非法路径: %q\n", f.name)
			}
			st.skip++
			continue
		}
		if f.info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return st, fmt.Errorf("创建目录失败 %q: %w", target, err)
			}
			st.dirs++
			continue
		}
		// symlink：zip 里以 symlink 模式存放，内容即链接目标。
		if f.info.Mode()&os.ModeSymlink != 0 {
			if e := extractZipSymlink(f, target, opt, &st); e != nil {
				return st, e
			}
			continue
		}
		if err := extractZipFile(f, target, opt, &st); err != nil {
			// 已存在且非 force：记为跳过并继续，其它错误直接中断。
			if opt.skipExisting() && os.IsExist(err) {
				if !opt.quiet {
					fmt.Fprintf(os.Stderr, "跳过已存在: %s（用 -ao a 或 -f 覆盖）\n", target)
				}
				st.skip++
				continue
			}
			return st, err
		}
	}
	return st, nil
}

func extractZipSymlink(f *zipEntry, target string, opt options, st *stats) error {
	rc, err := f.open()
	if err != nil {
		return fmt.Errorf("读取 symlink 失败 %q: %w%s", f.name, err, f.hint)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, 64*1024))
	rc.Close()
	if err != nil {
		return fmt.Errorf("读取 symlink 失败 %q: %w%s", f.name, err, f.hint)
	}
	link := strings.TrimSpace(string(raw))
	if link == "" {
		return fmt.Errorf("symlink 目标为空 %q", f.name)
	}
	if !opt.quiet {
		fmt.Printf("l %s -> %s\n", target, link)
	}
	return createSymlink(target, link, opt, st)
}

func extractZipFile(f *zipEntry, target string, opt options, st *stats) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	rc, err := f.open()
	if err != nil {
		return fmt.Errorf("读取失败 %q: %w%s", f.name, err, f.hint)
	}
	defer rc.Close()

	resolved, err := resolveTarget(target, opt)
	if err != nil {
		return err
	}
	target = resolved
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if opt.overwriteMode() == owSkip {
		flag |= os.O_EXCL
	}
	out, err := os.OpenFile(target, flag, f.info.Mode().Perm())
	if err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return fmt.Errorf("创建文件失败 %q: %w", target, err)
	}
	n, copyErr := io.CopyBuffer(out, rc, make([]byte, 128*1024))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target) // 失败不留半截文件
		return fmt.Errorf("写入失败 %q: %w%s", target, copyErr, f.hint)
	}
	if syncErr != nil {
		return syncErr
	}
	if err := closeErr; err != nil {
		return err
	}
	// 尽量还原修改时间与权限（Windows 上 chmod 基本无害）。
	if !f.modTime.IsZero() {
		_ = os.Chtimes(target, time.Now(), f.modTime)
	}
	applyMOTW(target, opt)
	st.files++
	st.bytes += n
	if !opt.quiet {
		fmt.Printf("x %s\n", target)
	}
	return nil
}

// openTarStream 打开 tar 文件并按格式包上压缩层；cleanup 非空时在使用完后调用。
// extractTar / listTar / testTar 共用，保证三种入口的解压行为一致。
func openTarStream(in *os.File, f format) (io.Reader, func(), error) {
	var r io.Reader = in
	cleanup := func() {}
	switch f {
	case fmtTarGz:
		gr, err := gzip.NewReader(in)
		if err != nil {
			return nil, nil, fmt.Errorf("gzip 头损坏或不是 gzip 文件: %w", err)
		}
		r = gr
		cleanup = func() { _ = gr.Close() }
	case fmtTarBz2:
		r = bzip2.NewReader(in)
	case fmtTarXz:
		xr, err := xz.NewReader(in)
		if err != nil {
			return nil, nil, fmt.Errorf("xz 数据损坏或不是 xz 文件: %w", err)
		}
		r = xr
	case fmtTarZst:
		zr, err := zstd.NewReader(in)
		if err != nil {
			return nil, nil, fmt.Errorf("zstd 数据损坏或不是 zstd 文件: %w", err)
		}
		r = zr
		cleanup = func() { zr.Close() }
	}
	return r, cleanup, nil
}

// extractTar 解压 tar / tar.gz / tar.bz2 / tar.xz / tar.zst。
func extractTar(archivePath string, f format, outDir string, opt options) (stats, error) {
	var st stats
	in, err := os.Open(archivePath)
	if err != nil {
		return st, err
	}
	defer in.Close()

	r, cleanup, err := openTarStream(in, f)
	if err != nil {
		return st, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, fmt.Errorf("读取 tar 失败: %w", err)
		}
		if !shouldExtract(hdr.Name, opt) {
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		target, ok := safeJoinOpt(outDir, hdr.Name, opt)
		if !ok {
			if !opt.quiet {
				fmt.Fprintf(os.Stderr, "跳过非法路径: %q\n", hdr.Name)
			}
			st.skip++
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode).Perm()); err != nil {
				return st, fmt.Errorf("创建目录失败 %q: %w", target, err)
			}
			_ = os.Chtimes(target, time.Now(), hdr.ModTime)
			st.dirs++
			if !opt.quiet {
				fmt.Printf("d %s\n", target)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := extractTarFile(tr, target, hdr, opt, &st); err != nil {
				if opt.skipExisting() && os.IsExist(err) {
					if !opt.quiet {
						fmt.Fprintf(os.Stderr, "跳过已存在: %s（用 -ao a 或 -f 覆盖）\n", target)
					}
					st.skip++
					_, _ = io.Copy(io.Discard, tr)
					continue
				}
				return st, err
			}
		case tar.TypeSymlink, tar.TypeLink:
			link := hdr.Linkname
			if link == "" {
				if !opt.quiet {
					fmt.Fprintf(os.Stderr, "跳过空链接: %q\n", hdr.Name)
				}
				st.skip++
				continue
			}
			if !opt.quiet {
				fmt.Printf("l %s -> %s\n", target, link)
			}
			if err := createSymlink(target, link, opt, &st); err != nil {
				return st, err
			}
		default:
			// 设备节点、fifo 等：为安全起见跳过。
			if !opt.quiet {
				fmt.Fprintf(os.Stderr, "跳过特殊类型 %q (type=%c)\n", hdr.Name, hdr.Typeflag)
			}
			st.skip++
		}
	}
	return st, nil
}

func extractTarFile(tr *tar.Reader, target string, hdr *tar.Header, opt options, st *stats) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	resolved, err := resolveTarget(target, opt)
	if err != nil {
		return err
	}
	target = resolved
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if opt.overwriteMode() == owSkip {
		flag |= os.O_EXCL
	}
	perm := os.FileMode(hdr.Mode).Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, flag, perm)
	if err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return fmt.Errorf("创建文件失败 %q: %w", target, err)
	}
	n, copyErr := io.CopyBuffer(out, tr, make([]byte, 128*1024))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target) // 失败不留半截文件
		return fmt.Errorf("写入失败 %q: %w", target, copyErr)
	}
	if syncErr != nil {
		return syncErr
	}
	if err := closeErr; err != nil {
		return err
	}
	_ = os.Chmod(target, perm)
	_ = os.Chtimes(target, time.Now(), hdr.ModTime)
	applyMOTW(target, opt)
	st.files++
	st.bytes += n
	if !opt.quiet {
		fmt.Printf("x %s\n", target)
	}
	return nil
}

// createSymlink 创建符号链接；Windows 上常因权限失败，此时退化为跳过+警告。
func createSymlink(target, link string, opt options, st *stats) error {
	// 安全校验（参考 7-Zip 25.01 的 CVE-2025-55188）：
	// 拒绝链接目标跳出输出根目录的绝对路径与 ../ 穿越。
	if !safeSymlinkTarget(target, link, opt.out) {
		fmt.Fprintf(os.Stderr, "跳过危险 symlink: %s -> %s\n", target, link)
		st.skip++
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		if !opt.skipExisting() {
			st.skip++
			return nil
		}
		_ = os.Remove(target)
	}
	if err := os.Symlink(link, target); err != nil {
		// Windows 普通用户默认无 mklink 权限：给警告而不是直接失败。
		fmt.Fprintf(os.Stderr, "警告: 创建 symlink 失败 %s -> %s: %v\n", target, link, err)
		st.skip++
		return nil
	}
	st.files++
	return nil
}

// safeSymlinkTarget 检查 symlink 目标是否可能跳出输出目录。
// 绝对目标或含 .. 穿越的目标一律拒绝（保守策略）。
func safeSymlinkTarget(target, link, outRoot string) bool {
	if link == "" {
		return false
	}
	if filepath.IsAbs(link) || strings.HasPrefix(link, "/") || strings.HasPrefix(link, "\\") {
		return false
	}
	if len(link) >= 2 && link[1] == ':' {
		return false
	}
	clean := filepath.Clean(strings.ReplaceAll(link, "\\", "/"))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "./../") {
		return false
	}
	// 计算解析后的绝对路径，确保仍在输出根内。
	base := outRoot
	if base == "" {
		base = "."
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	absLink := filepath.Join(filepath.Dir(target), filepath.FromSlash(clean))
	absLink, err = filepath.Abs(absLink)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absBase, absLink)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// listArchive 只列出压缩包内容，不解压。
func listArchive(archivePath string, f format, opt options) error {
	switch f {
	case fmtZip:
		return listZip(archivePath, opt.quiet)
	case fmtTar, fmtTarGz, fmtTarBz2, fmtTarXz, fmtTarZst:
		return listTar(archivePath, f)
	case fmtRar:
		return listRar(archivePath, opt)
	case fmtSevenZ:
		return listSevenZ(archivePath, opt.quiet)
	case fmtGz, fmtBz2, fmtXz, fmtZst, fmtLZ4, fmtBrotli, fmtLZ, fmtLZMA:
		fi, _ := os.Stat(archivePath)
		base := stripArchiveSuffix(filepath.Base(archivePath))
		if isExtraSingle(f) {
			base = stripExtraSuffix(filepath.Base(archivePath))
		}
		fmt.Printf("%12d  %s (单文件流，解压后约名: %s)\n", fi.Size(), archivePath, base)
		return nil
	default:
		return fmt.Errorf("未知格式")
	}
}

func listZip(archivePath string, quiet bool) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	if !quiet {
		fmt.Printf("%-10s %10s %10s  %s\n", "方法", "压缩", "原始", "名称")
	}
	var totalC, totalU int64
	for _, f := range zr.File {
		fmt.Printf("%-10d %10d %10d  %s\n", f.Method, f.CompressedSize64, f.UncompressedSize64, f.Name)
		totalC += int64(f.CompressedSize64)
		totalU += int64(f.UncompressedSize64)
	}
	fmt.Printf("共 %d 项，压缩 %d 字节 / 原始 %d 字节\n", len(zr.File), totalC, totalU)
	return nil
}

func listTar(archivePath string, f format) error {
	in, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer in.Close()
	r, cleanup, err := openTarStream(in, f)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	tr := tar.NewReader(r)
	n := 0
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		fmt.Printf("%8d  %s  %s\n", hdr.Size, hdr.ModTime.Format("2006-01-02 15:04"), hdr.Name)
		n++
		total += hdr.Size
		_, _ = io.Copy(io.Discard, tr)
	}
	fmt.Printf("共 %d 项，原始约 %d 字节\n", n, total)
	return nil
}

// ---- 完整性测试（-t）----

// testArchive 校验压缩包完整性：读出全部数据但不落盘（类似 7z t）。
// zip 校验每个条目的 CRC / AES 认证码；tar 与单文件流校验解压全程
// （gzip / xz / zstd 尾部校验值在读到流末尾时验证）；rar / 7z 校验包内 CRC。
func testArchive(archivePath string, f format, opt options) (stats, error) {
	switch f {
	case fmtZip:
		return testZip(archivePath, opt)
	case fmtTar, fmtTarGz, fmtTarBz2, fmtTarXz, fmtTarZst:
		return testTar(archivePath, f, opt)
	case fmtGz, fmtBz2, fmtXz, fmtZst, fmtLZ4, fmtBrotli, fmtLZ, fmtLZMA:
		return testSingle(archivePath, f, opt)
	case fmtRar:
		return testRar(archivePath, opt)
	case fmtSevenZ:
		return testSevenZ(archivePath, opt)
	default:
		return stats{}, fmt.Errorf("未知格式")
	}
}

func testZip(archivePath string, opt options) (stats, error) {
	var st stats
	entries, closeZip, err := openZipEntries(archivePath, opt)
	if err != nil {
		return st, err
	}
	defer closeZip()
	failed := 0
	for _, e := range entries {
		if e.info.IsDir() {
			st.dirs++
			continue
		}
		rc, err := e.open()
		if err != nil {
			fmt.Fprintf(os.Stderr, "校验失败: %s (%v)%s\n", e.name, err, e.hint)
			failed++
			continue
		}
		n, err := io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "校验失败: %s (%v)%s\n", e.name, err, e.hint)
			failed++
			continue
		}
		st.files++
		st.bytes += n
	}
	if failed > 0 {
		return st, fmt.Errorf("%d 个条目校验未通过（共 %d 个条目）", failed, len(entries))
	}
	return st, nil
}

func testTar(archivePath string, f format, opt options) (stats, error) {
	var st stats
	in, err := os.Open(archivePath)
	if err != nil {
		return st, err
	}
	defer in.Close()
	r, cleanup, err := openTarStream(in, f)
	if err != nil {
		return st, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, fmt.Errorf("读取 tar 失败: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			st.dirs++
			continue
		}
		n, err := io.Copy(io.Discard, tr)
		if err != nil {
			return st, fmt.Errorf("校验失败 %q: %w", hdr.Name, err)
		}
		st.files++
		st.bytes += n
	}
	return st, nil
}

func testSingle(archivePath string, f format, opt options) (stats, error) {
	var st stats
	in, err := os.Open(archivePath)
	if err != nil {
		return st, err
	}
	defer in.Close()
	wrap := singleWrapper(f)
	if wrap == nil {
		return st, fmt.Errorf("未知格式")
	}
	r, cleanup, err := wrap(in)
	if err != nil {
		return st, err
	}
	if cleanup != nil {
		defer cleanup()
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		return st, fmt.Errorf("校验失败: %w", err)
	}
	st.files = 1
	st.bytes = n
	return st, nil
}
