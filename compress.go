package main

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	aeszip "github.com/alexmullins/zip" // archive/zip 的 AES 扩展 fork：仅在创建加密 zip 时使用
	dsnetbzip2 "github.com/dsnet/compress/bzip2"
)

// arcEntry 是待打包的一个条目：本地路径 + 包内路径。
type arcEntry struct {
	arcPath string // 包内路径，/ 分隔
	fsPath  string // 本地路径
	info    fs.FileInfo
	isDir   bool
	isLink  bool
	link    string // isLink 时为链接目标
}

// compressArchive 把文件/目录压缩成 outPath（格式按后缀判断）。
func compressArchive(outPath string, inputs []string, opt options) (stats, error) {
	var st stats
	f := detectFormat(outPath)
	switch f {
	case fmtZip, fmtSevenZ, fmtTar, fmtTarGz, fmtTarBz2, fmtTarXz, fmtTarZst, fmtGz, fmtBz2, fmtXz, fmtZst,
		fmtLZ4, fmtBrotli, fmtLZ, fmtLZMA:
		// 可写格式，继续。
	case fmtRar:
		return st, fmt.Errorf("不支持创建 rar 格式（专有格式，7-Zip 本身也不创建 rar）")
	default:
		return st, fmt.Errorf("无法按后缀判断压缩格式 %q", outPath)
	}
	if fi, err := os.Stat(outPath); err == nil {
		if fi.IsDir() {
			return st, fmt.Errorf("输出 %q 是一个已存在的目录", outPath)
		}
		if opt.skipExisting() {
			return st, fmt.Errorf("输出已存在 %q（用 -ao a 或 -f 覆盖）", outPath)
		}
	}

	// 单文件流格式：只接受 exactly 一个文件输入。
	if !isContainer(f) {
		if len(inputs) != 1 {
			return st, fmt.Errorf("%s 是单文件压缩格式，一次只能压缩一个文件（给了 %d 个输入）", formatName(f), len(inputs))
		}
		if fi, err := os.Stat(inputs[0]); err != nil {
			return st, fmt.Errorf("无法访问 %q: %w", inputs[0], err)
		} else if fi.IsDir() {
			return st, fmt.Errorf("%s 是单文件压缩格式，不能压缩目录 %q（请改用 .tar.gz 等容器格式）", formatName(f), inputs[0])
		}
		if err := compressSingle(outPath, inputs[0], f, opt); err != nil {
			return st, err
		}
		if opt.volumes != "" {
			if err := splitVolumes(outPath, opt.volumes); err != nil {
				return st, fmt.Errorf("分卷失败: %w", err)
			}
			_ = os.Remove(outPath)
		}
		if !opt.quiet {
			fmt.Printf("已压缩: %s -> %s\n", inputs[0], outPath)
		}
		st.files = 1
		if fi, e := os.Stat(outPath); e == nil {
			st.bytes = fi.Size()
		}
		return st, nil
	}

	entries, err := collectEntries(inputs)
	if err != nil {
		return st, err
	}
	if len(entries) == 0 {
		return st, fmt.Errorf("没有可压缩的内容")
	}

	switch f {
	case fmtZip:
		st, err = writeZip(outPath, entries, opt)
	case fmtSevenZ:
		st, err = write7z(outPath, entries, opt)
	default:
		st, err = writeTar(outPath, f, entries, opt)
	}
	if err != nil {
		return st, err
	}
	// -stl：把压缩包时间戳设为输入文件中最新的修改时间。
	if opt.stl {
		var latest time.Time
		for _, e := range entries {
			if e.info.ModTime().After(latest) {
				latest = e.info.ModTime()
			}
		}
		if !latest.IsZero() {
			_ = os.Chtimes(outPath, latest, latest)
		}
	}
	// -v 分卷：写完后切分，删除未切分的整包。
	if opt.volumes != "" {
		if err := splitVolumes(outPath, opt.volumes); err != nil {
			return st, fmt.Errorf("分卷失败: %w", err)
		}
		_ = os.Remove(outPath)
		if !opt.quiet {
			fmt.Printf("已生成分卷: %s.* （%s/卷）\n", outPath, opt.volumes)
		}
	} else if !opt.quiet {
		fmt.Printf("已生成: %s （%d 文件 / %d 目录 / %d 字节）\n", outPath, st.files, st.dirs, st.bytes)
	}
	return st, nil
}

// topName 推导输入在包内的顶层名：在当前目录内就保留相对路径，
// 在当前目录外则只取基名（避免包内出现 ../），输入为 "." 时返回 ""。
func topName(input, cwd string) string {
	if abs, err := filepath.Abs(input); err == nil {
		if rel, err := filepath.Rel(cwd, abs); err == nil &&
			rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	if filepath.Clean(input) == "." {
		return ""
	}
	return filepath.Base(filepath.Clean(input))
}

// joinArc 拼接包内路径（/ 分隔）。
func joinArc(prefix, rel string) string {
	srel := filepath.ToSlash(rel)
	if prefix == "" {
		return srel
	}
	if srel == "." || srel == "" {
		return prefix
	}
	return prefix + "/" + srel
}

// collectEntries 把输入的文件/目录展开成条目列表（目录递归，遇到重复包内路径时首个生效）。
func collectEntries(inputs []string) ([]arcEntry, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	var entries []arcEntry
	seen := map[string]bool{}
	add := func(e arcEntry) {
		if e.arcPath == "" || e.arcPath == "." || seen[e.arcPath] {
			return
		}
		seen[e.arcPath] = true
		entries = append(entries, e)
	}

	for _, in := range inputs {
		c := filepath.Clean(in)
		li, err := os.Lstat(c)
		if err != nil {
			return nil, fmt.Errorf("无法访问 %q: %w", in, err)
		}
		prefix := topName(c, cwd)
		// 顶层 symlink：存链接本身。
		if li.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(c)
			if err != nil {
				return nil, fmt.Errorf("读取链接失败 %q: %w", in, err)
			}
			add(arcEntry{arcPath: prefix, fsPath: c, info: li, isLink: true, link: link})
			continue
		}
		if !li.IsDir() {
			add(arcEntry{arcPath: prefix, fsPath: c, info: li})
			continue
		}
		// 目录：递归（含空目录本身；WalkDir 不跟随 symlink 目录）。
		err = filepath.WalkDir(c, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(c, p)
			if err != nil {
				return err
			}
			arc := joinArc(prefix, rel)
			if arc == "" || arc == "." {
				return nil // 输入就是 "." 时跳过根目录自身
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				link, err := os.Readlink(p)
				if err != nil {
					return fmt.Errorf("读取链接失败 %q: %w", p, err)
				}
				add(arcEntry{arcPath: arc, fsPath: p, info: info, isLink: true, link: link})
				return nil
			}
			if d.IsDir() {
				add(arcEntry{arcPath: arc, fsPath: p, info: info, isDir: true})
				return nil
			}
			add(arcEntry{arcPath: arc, fsPath: p, info: info})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("遍历目录失败 %q: %w", in, err)
		}
	}
	return entries, nil
}

// openCreate 创建输出文件（非 force 时 O_EXCL 防覆盖）。
func openCreate(path string, opt options) (*os.File, error) {
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if opt.skipExisting() {
		flag |= os.O_EXCL
	}
	out, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("输出已存在 %q（用 -ao a 或 -f 覆盖）", path)
		}
		return nil, err
	}
	return out, nil
}

// removePartial 失败时删掉半成品输出。
func removePartial(path string) { _ = os.Remove(path) }

// compressSingle 压缩单个文件为 gz / bz2 / xz 单文件流。
func compressSingle(outPath, inPath string, f format, opt options) error {
	in, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := openCreate(outPath, opt)
	if err != nil {
		return err
	}
	var w io.Writer = out
	var finish func() error
	switch f {
	case fmtGz:
		gw, err := newGzipWriter(out, opt)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = gw, gw.Close
	case fmtXz:
		xw, err := newXzWriter(out, opt)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = xw, xw.Close
	case fmtBz2:
		bw, err := dsnetbzip2.NewWriter(out, &dsnetbzip2.WriterConfig{Level: levelOrDefault(opt, 6)})
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = bw, bw.Close
	case fmtZst:
		zw, err := newZstdWriter(out, opt)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = zw, zw.Close
	case fmtLZ4:
		lw, err := newLZ4Writer(out)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = lw, lw.Close
	case fmtBrotli:
		bw, err := newBrotliWriter(out, opt)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = bw, bw.Close
	case fmtLZ:
		lw, err := newLZIPWriter(out)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = lw, lw.Close
	case fmtLZMA:
		lw, err := newLZMAWriter(out)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return err
		}
		w, finish = lw, lw.Close
	default:
		out.Close()
		return fmt.Errorf("未知格式")
	}
	_, copyErr := io.CopyBuffer(w, in, make([]byte, 128*1024))
	if finish != nil {
		if cerr := finish(); copyErr == nil {
			copyErr = cerr
		}
	}
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		removePartial(outPath)
		return fmt.Errorf("压缩失败: %w", copyErr)
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// writeZip 打包为 zip（目录、空目录、symlink 均保留，可被本工具解压还原）。
// 带 -p 密码时改走 writeZipAES，创建 WinZip AES-256 加密 zip。
func writeZip(outPath string, entries []arcEntry, opt options) (stats, error) {
	var st stats
	out, err := openCreate(outPath, opt)
	if err != nil {
		return st, err
	}
	if opt.password != "" {
		return writeZipAES(out, outPath, entries, opt)
	}
	zw := zip.NewWriter(out)
	fail := func(err error) (stats, error) {
		zw.Close()
		out.Close()
		removePartial(outPath)
		return stats{}, err
	}
	for _, e := range entries {
		// FileInfoHeader 自动带上权限/时间/类型（含 symlink 与目录）。
		fh, err := zip.FileInfoHeader(e.info)
		if err != nil {
			return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
		}
		fh.Name = e.arcPath
		if e.isDir {
			if !strings.HasSuffix(fh.Name, "/") {
				fh.Name += "/"
			}
			fh.Method = zip.Store
			if _, err := zw.CreateHeader(fh); err != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
			}
			st.dirs++
			if !opt.quiet {
				fmt.Printf("a %s\n", e.arcPath)
			}
			continue
		}
		n, err := writeZipEntry(zw, fh, e, opt)
		if err != nil {
			return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
		}
		st.files++
		st.bytes += n
		if !opt.quiet {
			fmt.Printf("a %s\n", e.arcPath)
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		removePartial(outPath)
		return stats{}, fmt.Errorf("打包失败: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return stats{}, err
	}
	if err := out.Close(); err != nil {
		return stats{}, err
	}
	return st, nil
}

// writeZipEntry 把单个文件/链接条目写入 zip。
// 当 -level 指定且方法为 deflate 时，用 CreateRaw + 自压缩数据绕过
// archive/zip 全局注册的默认压缩器（stdlib 在 init 时已注册，无法覆盖）。
func writeZipEntry(zw *zip.Writer, fh *zip.FileHeader, e arcEntry, opt options) (int64, error) {
	method := zipMethod(opt)
	fh.Method = method
	// 默认级别：直接用 CreateHeader 让标准库压缩。
	if opt.level < 0 || method == zip.Store {
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return 0, err
		}
		return writeEntryData(w, e)
	}
	// 自定义级别：先压缩到临时文件，同时算 CRC，再用 CreateRaw 写入。
	tmp, err := os.CreateTemp("", "xunzip-deflate-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	crc := crc32.NewIEEE()
	src, n, err := entryReader(e)
	if err != nil {
		return 0, err
	}
	fw, err := flate.NewWriter(io.MultiWriter(tmp, crc), flateLevel(opt))
	if err != nil {
		src.Close()
		return 0, err
	}
	_, copyErr := io.Copy(fw, src)
	src.Close()
	if cerr := fw.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		return 0, copyErr
	}
	compSize, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	fh.Flags |= 0x08 // data descriptor 关闭，使用 header 中的 CRC/大小
	fh.CRC32 = crc.Sum32()
	fh.CompressedSize64 = uint64(compSize)
	fh.UncompressedSize64 = uint64(n)
	raw, err := zw.CreateRaw(fh)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(raw, tmp); err != nil {
		return 0, err
	}
	return n, nil
}

// entryReader 返回条目的原始数据流与字节数。
func entryReader(e arcEntry) (io.ReadCloser, int64, error) {
	if e.isLink {
		return io.NopCloser(strings.NewReader(e.link)), int64(len(e.link)), nil
	}
	f, err := os.Open(e.fsPath)
	if err != nil {
		return nil, 0, err
	}
	return f, e.info.Size(), nil
}

// writeEntryData 把条目内容写入标准 CreateHeader 返回的 writer。
func writeEntryData(w io.Writer, e arcEntry) (int64, error) {
	if e.isLink {
		n, err := io.WriteString(w, e.link)
		return int64(n), err
	}
	in, err := os.Open(e.fsPath)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	return io.Copy(w, in)
}

// writeZipAES 创建 WinZip AES-256 加密 zip（目录条目不加密，文件/链接内容加密）。
// fork 库为老版 API，时间戳只有 MS-DOS 精度，因此普通 zip 仍走标准库。
func writeZipAES(out *os.File, outPath string, entries []arcEntry, opt options) (stats, error) {
	var st stats
	zw := aeszip.NewWriter(out)
	fail := func(err error) (stats, error) {
		zw.Close()
		out.Close()
		removePartial(outPath)
		return stats{}, err
	}
	for _, e := range entries {
		fh, err := aeszip.FileInfoHeader(e.info)
		if err != nil {
			return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
		}
		fh.Name = e.arcPath
		if e.isDir {
			if !strings.HasSuffix(fh.Name, "/") {
				fh.Name += "/"
			}
			fh.Method = aeszip.Store
			if _, err := zw.CreateHeader(fh); err != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
			}
			st.dirs++
			if !opt.quiet {
				fmt.Printf("a %s\n", e.arcPath)
			}
			continue
		}
		fh.Method = aeszip.Deflate
		fh.SetPassword(opt.password)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
		}
		var n int64
		if e.isLink {
			m, err := io.WriteString(w, e.link)
			n = int64(m)
			if err != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
			}
		} else {
			in, err := os.Open(e.fsPath)
			if err != nil {
				return fail(fmt.Errorf("读取失败 %q: %w", e.fsPath, err))
			}
			n, err = io.Copy(w, in)
			in.Close()
			if err != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
			}
		}
		st.files++
		st.bytes += n
		if !opt.quiet {
			fmt.Printf("a %s\n", e.arcPath)
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		removePartial(outPath)
		return stats{}, fmt.Errorf("打包失败: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return stats{}, err
	}
	if err := out.Close(); err != nil {
		return stats{}, err
	}
	return st, nil
}

// writeTar 打包为 tar / tar.gz / tar.bz2 / tar.xz / tar.zst。
func writeTar(outPath string, f format, entries []arcEntry, opt options) (stats, error) {
	var st stats
	out, err := openCreate(outPath, opt)
	if err != nil {
		return st, err
	}
	var w io.Writer = out
	var finish func() error
	switch f {
	case fmtTarGz:
		gw, err := newGzipWriter(out, opt)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return st, err
		}
		w, finish = gw, gw.Close
	case fmtTarBz2:
		bw, err := dsnetbzip2.NewWriter(out, &dsnetbzip2.WriterConfig{Level: levelOrDefault(opt, 6)})
		if err != nil {
			out.Close()
			removePartial(outPath)
			return st, err
		}
		w, finish = bw, bw.Close
	case fmtTarXz:
		xw, err := newXzWriter(out, opt)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return st, err
		}
		w, finish = xw, xw.Close
	case fmtTarZst:
		zw, err := newZstdWriter(out, opt)
		if err != nil {
			out.Close()
			removePartial(outPath)
			return st, err
		}
		w, finish = zw, zw.Close
	case fmtTar:
		// 无压缩层。
	default:
		out.Close()
		return st, fmt.Errorf("未知格式")
	}
	tw := tar.NewWriter(w)
	fail := func(err error) (stats, error) {
		tw.Close()
		if finish != nil {
			_ = finish()
		}
		out.Close()
		removePartial(outPath)
		return stats{}, err
	}
	for _, e := range entries {
		name := e.arcPath
		perm := e.info.Mode().Perm()
		hdr := &tar.Header{
			Name:    name,
			ModTime: e.info.ModTime(),
			Format:  tar.FormatPAX,
		}
		switch {
		case e.isDir:
			if !strings.HasSuffix(hdr.Name, "/") {
				hdr.Name += "/"
			}
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = int64(dirPerm(perm))
		case e.isLink:
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.link
			hdr.Mode = int64(filePerm(perm))
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = int64(filePerm(perm))
			hdr.Size = e.info.Size()
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
		}
		var n int64
		if !e.isDir && !e.isLink {
			in, err := os.Open(e.fsPath)
			if err != nil {
				return fail(fmt.Errorf("读取失败 %q: %w", e.fsPath, err))
			}
			n, err = io.Copy(tw, in)
			in.Close()
			if err != nil {
				return fail(fmt.Errorf("打包失败 %q: %w", e.arcPath, err))
			}
		}
		if e.isDir {
			st.dirs++
		} else {
			st.files++
			st.bytes += n
		}
		if !opt.quiet {
			fmt.Printf("a %s\n", e.arcPath)
		}
	}
	if err := tw.Close(); err != nil {
		if finish != nil {
			_ = finish()
		}
		out.Close()
		removePartial(outPath)
		return stats{}, fmt.Errorf("打包失败: %w", err)
	}
	if finish != nil {
		if err := finish(); err != nil {
			out.Close()
			removePartial(outPath)
			return stats{}, fmt.Errorf("打包失败: %w", err)
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return stats{}, err
	}
	if err := out.Close(); err != nil {
		return stats{}, err
	}
	return st, nil
}

func filePerm(p os.FileMode) os.FileMode {
	if p == 0 {
		return 0o644
	}
	return p
}

func dirPerm(p os.FileMode) os.FileMode {
	if p == 0 {
		return 0o755
	}
	return p
}
