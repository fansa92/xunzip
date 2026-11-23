package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

// writeStreamToFile 把流式内容落盘为目标文件，供 rar / 7z 这类顺序或随机读取的格式共用。
// 目标已存在且非 force 时返回 os.ErrExist（调用方需自行消费掉当前条目流并记为跳过）。
func writeStreamToFile(r io.Reader, target string, perm os.FileMode, modTime time.Time, opt options, st *stats) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建父目录失败: %w", err)
	}
	resolved, err := resolveTarget(target, opt)
	if err != nil {
		return err
	}
	target = resolved
	if perm == 0 {
		perm = 0o644
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if opt.overwriteMode() == owSkip {
		flag |= os.O_EXCL
	}
	out, err := os.OpenFile(target, flag, perm)
	if err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return fmt.Errorf("创建文件失败 %q: %w", target, err)
	}
	n, copyErr := io.CopyBuffer(out, r, make([]byte, 128*1024))
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
	if !modTime.IsZero() {
		_ = os.Chtimes(target, time.Now(), modTime)
	}
	st.files++
	st.bytes += n
	if !opt.quiet {
		fmt.Printf("x %s\n", target)
	}
	return nil
}

// rarOpenOpts 把 -p 密码转成 rardecode 的选项。
func rarOpenOpts(opt options) []rardecode.Option {
	if opt.password != "" {
		return []rardecode.Option{rardecode.Password(opt.password)}
	}
	return nil
}

// extractRar 解压 rar（含 RAR5、分卷首卷、solid 连续读取）。
// 注意 solid 包必须按 Next 顺序消费流，跳过条目时也要 discard 掉当前数据。
func extractRar(archivePath, outDir string, opt options) (stats, error) {
	var st stats
	rc, err := rardecode.OpenReader(archivePath, rarOpenOpts(opt)...)
	if err != nil {
		return st, fmt.Errorf("打开 rar 失败: %w%s", err, passwordHint(err, opt))
	}
	defer rc.Close()

	for {
		hdr, err := rc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, fmt.Errorf("读取 rar 失败: %w%s", err, passwordHint(err, opt))
		}
		if !shouldExtract(hdr.Name, opt) {
			_, _ = io.Copy(io.Discard, rc)
			continue
		}
		target, ok := safeJoinOpt(outDir, hdr.Name, opt)
		if !ok {
			if !opt.quiet {
				fmt.Fprintf(os.Stderr, "跳过非法路径: %q\n", hdr.Name)
			}
			st.skip++
			_, _ = io.Copy(io.Discard, rc)
			continue
		}
		if hdr.IsDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return st, fmt.Errorf("创建目录失败 %q: %w", target, err)
			}
			st.dirs++
			if !opt.quiet {
				fmt.Printf("d %s\n", target)
			}
			continue
		}
		if hdr.Encrypted && opt.password == "" {
			return st, fmt.Errorf("条目 %q 已加密，请用 -p 指定密码", hdr.Name)
		}
		// 链接类型：内容即链接目标，不经过数据流（仍需消费流以防 solid 错位）。
		switch hdr.LinkType {
		case rardecode.LinkTypeUnixSymlink, rardecode.LinkTypeWindowsSymlink, rardecode.LinkTypeWindowsJunction:
			_, _ = io.Copy(io.Discard, rc)
			if hdr.LinkTarget == "" {
				if !opt.quiet {
					fmt.Fprintf(os.Stderr, "跳过空链接: %q\n", hdr.Name)
				}
				st.skip++
				continue
			}
			if !opt.quiet {
				fmt.Printf("l %s -> %s\n", target, hdr.LinkTarget)
			}
			if err := createSymlink(target, hdr.LinkTarget, opt, &st); err != nil {
				return st, err
			}
			continue
		}
		if err := writeStreamToFile(rc, target, hdr.Mode().Perm(), hdr.ModificationTime, opt, &st); err != nil {
			if opt.skipExisting() && os.IsExist(err) {
				if !opt.quiet {
					fmt.Fprintf(os.Stderr, "跳过已存在: %s（用 -ao a 或 -f 覆盖）\n", target)
				}
				st.skip++
				_, _ = io.Copy(io.Discard, rc)
				continue
			}
			return st, err
		}
	}
	return st, nil
}

// passwordHint 遇到加密相关错误且用户没给密码时，提示 -p 参数。
func passwordHint(err error, opt options) string {
	if opt.password != "" {
		return ""
	}
	if errors.Is(err, rardecode.ErrArchiveEncrypted) || errors.Is(err, rardecode.ErrArchivedFileEncrypted) {
		return "（压缩包已加密，请用 -p 指定密码）"
	}
	var rerr *sevenzip.ReadError
	if errors.As(err, &rerr) && rerr.Encrypted {
		return "（压缩包可能已加密，请用 -p 指定密码）"
	}
	return ""
}

// listRar 只列出 rar 内容。
func listRar(archivePath string, opt options) error {
	files, err := rardecode.List(archivePath, rarOpenOpts(opt)...)
	if err != nil {
		return fmt.Errorf("%w%s", err, passwordHint(err, opt))
	}
	var total int64
	for _, f := range files {
		name := f.Name
		if f.IsDir {
			name += "/"
		}
		fmt.Printf("%8d  %s  %s\n", f.UnPackedSize, f.ModificationTime.Format("2006-01-02 15:04"), name)
		total += f.UnPackedSize
	}
	fmt.Printf("共 %d 项，原始约 %d 字节\n", len(files), total)
	return nil
}

// extractSevenZ 解压 7z。
// 注意：7z 常把多文件压成一条流，按包内自然顺序解压最快；每个条目的 rc 必须在
// 打开下一个之前 Close，否则会丢失库内的流复用优化。
func extractSevenZ(archivePath, outDir string, opt options) (stats, error) {
	var st stats
	var zr *sevenzip.ReadCloser
	var err error
	if opt.password != "" {
		zr, err = sevenzip.OpenReaderWithPassword(archivePath, opt.password)
	} else {
		zr, err = sevenzip.OpenReader(archivePath)
	}
	if err != nil {
		return st, fmt.Errorf("打开 7z 失败: %w%s", err, passwordHint(err, opt))
	}
	defer zr.Close()

	for _, f := range zr.File {
		if !shouldExtract(f.Name, opt) {
			continue
		}
		target, ok := safeJoinOpt(outDir, f.Name, opt)
		if !ok {
			if !opt.quiet {
				fmt.Fprintf(os.Stderr, "跳过非法路径: %q\n", f.Name)
			}
			st.skip++
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return st, fmt.Errorf("创建目录失败 %q: %w", target, err)
			}
			st.dirs++
			if !opt.quiet {
				fmt.Printf("d %s\n", target)
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return st, fmt.Errorf("读取失败 %q: %w%s", f.Name, err, passwordHint(err, opt))
		}
		// symlink：内容即链接目标。
		if f.Mode()&os.ModeSymlink != 0 {
			raw, rerr := io.ReadAll(io.LimitReader(rc, 64*1024))
			rc.Close()
			if rerr != nil {
				return st, fmt.Errorf("读取 symlink 失败 %q: %w", f.Name, rerr)
			}
			link := string(raw)
			if link == "" {
				st.skip++
				continue
			}
			if !opt.quiet {
				fmt.Printf("l %s -> %s\n", target, link)
			}
			if err := createSymlink(target, link, opt, &st); err != nil {
				return st, err
			}
			continue
		}
		werr := writeStreamToFile(rc, target, f.Mode().Perm(), f.Modified, opt, &st)
		rc.Close() // 必须在下一个条目前关闭，以复用解压流
		if werr != nil {
			if opt.skipExisting() && os.IsExist(werr) {
				if !opt.quiet {
					fmt.Fprintf(os.Stderr, "跳过已存在: %s（用 -ao a 或 -f 覆盖）\n", target)
				}
				st.skip++
				continue
			}
			return st, werr
		}
	}
	return st, nil
}

// listSevenZ 只列出 7z 内容。
func listSevenZ(archivePath string, quiet bool) error {
	var zr *sevenzip.ReadCloser
	var err error
	// 列出操作不需要密码也能读到目录（除非包头加密，此时直接报错提示）。
	zr, err = sevenzip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("%w%s", err, passwordHint(err, options{}))
	}
	defer zr.Close()
	if !quiet {
		fmt.Printf("%10s  %s\n", "原始", "名称")
	}
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			name += "/"
		}
		fmt.Printf("%10d  %s\n", f.UncompressedSize, name)
		total += f.UncompressedSize
	}
	fmt.Printf("共 %d 项，原始约 %d 字节\n", len(zr.File), total)
	return nil
}

// testRar 校验 rar 完整性：按顺序读出全部条目数据但不落盘（solid 包必须顺序读）。
func testRar(archivePath string, opt options) (stats, error) {
	var st stats
	rc, err := rardecode.OpenReader(archivePath, rarOpenOpts(opt)...)
	if err != nil {
		return st, fmt.Errorf("打开 rar 失败: %w%s", err, passwordHint(err, opt))
	}
	defer rc.Close()

	for {
		hdr, err := rc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, fmt.Errorf("读取 rar 失败: %w%s", err, passwordHint(err, opt))
		}
		if hdr.IsDir {
			st.dirs++
			continue
		}
		if hdr.Encrypted && opt.password == "" {
			return st, fmt.Errorf("条目 %q 已加密，请用 -p 指定密码", hdr.Name)
		}
		n, err := io.Copy(io.Discard, rc)
		if err != nil {
			return st, fmt.Errorf("校验失败 %q: %w", hdr.Name, err)
		}
		st.files++
		st.bytes += n
	}
	return st, nil
}

// testSevenZ 校验 7z 完整性：读出全部条目数据但不落盘（验证包内 CRC）。
// 与解压相同，每个条目的流必须在下一个之前 Close。
func testSevenZ(archivePath string, opt options) (stats, error) {
	var st stats
	var zr *sevenzip.ReadCloser
	var err error
	if opt.password != "" {
		zr, err = sevenzip.OpenReaderWithPassword(archivePath, opt.password)
	} else {
		zr, err = sevenzip.OpenReader(archivePath)
	}
	if err != nil {
		return st, fmt.Errorf("打开 7z 失败: %w%s", err, passwordHint(err, opt))
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			st.dirs++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return st, fmt.Errorf("读取失败 %q: %w%s", f.Name, err, passwordHint(err, opt))
		}
		n, cerr := io.Copy(io.Discard, rc)
		rc.Close()
		if cerr != nil {
			return st, fmt.Errorf("校验失败 %q: %w", f.Name, cerr)
		}
		st.files++
		st.bytes += n
	}
	return st, nil
}
