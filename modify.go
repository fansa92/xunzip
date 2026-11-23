package main

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ---- d 命令：删除包内条目 ----

func runDelete(p parsedCLI) int {
	if len(p.args) < 2 {
		fmt.Fprintln(os.Stderr, "错误: d 命令需要 <压缩包> <条目>...")
		return 7
	}
	arc := p.args[0]
	patterns := p.args[1:]
	f := resolveFormat(arc, p.opt)
	if f != fmtZip && !isTar(f) {
		fmt.Fprintln(os.Stderr, "删除条目仅支持 zip/tar 系列（rar/7z 不支持包内修改）")
		return 2
	}
	if err := ensureArchiveExists(arc); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		return 2
	}
	remove := func(name string) bool {
		for _, pat := range patterns {
			if matchPattern(pat, name) || pat == name {
				return true
			}
		}
		return false
	}
	var err error
	if f == fmtZip {
		err = rewriteZip(arc, nil, remove, nil, p.opt)
	} else {
		err = rewriteTar(arc, f, nil, remove, nil, p.opt)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		return 2
	}
	return 0
}

// ---- u 命令：更新（添加或替换）包内条目 ----

func runUpdate(p parsedCLI) int {
	if len(p.args) < 2 {
		fmt.Fprintln(os.Stderr, "错误: u 命令需要 <压缩包> <文件>...")
		return 7
	}
	arc := p.args[0]
	inputs := p.args[1:]
	f := resolveFormat(arc, p.opt)
	if f != fmtZip && !isTar(f) {
		fmt.Fprintln(os.Stderr, "更新条目仅支持 zip/tar 系列（rar/7z 不支持包内修改）")
		return 2
	}
	entries, err := collectEntries(inputs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		return 2
	}
	// 更新 = 删除同名条目 + 追加新条目。
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.arcPath] = true
	}
	remove := func(name string) bool { return names[name] }
	if f == fmtZip {
		err = rewriteZip(arc, entries, remove, nil, p.opt)
	} else {
		err = rewriteTar(arc, f, entries, remove, nil, p.opt)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		return 2
	}
	return 0
}

// ---- rn 命令：重命名包内条目 ----

func runRename(p parsedCLI) int {
	if len(p.args) != 3 {
		fmt.Fprintln(os.Stderr, "错误: rn 命令需要 <压缩包> <旧名> <新名>")
		return 7
	}
	arc, oldName, newName := p.args[0], p.args[1], p.args[2]
	f := resolveFormat(arc, p.opt)
	if f != fmtZip && !isTar(f) {
		fmt.Fprintln(os.Stderr, "重命名条目仅支持 zip/tar 系列")
		return 2
	}
	rename := func(name string) string {
		if name == oldName {
			return newName
		}
		return name
	}
	var err error
	if f == fmtZip {
		err = rewriteZip(arc, nil, nil, rename, p.opt)
	} else {
		err = rewriteTar(arc, f, nil, nil, rename, p.opt)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		return 2
	}
	return 0
}

func isTar(f format) bool {
	switch f {
	case fmtTar, fmtTarGz, fmtTarBz2, fmtTarXz, fmtTarZst:
		return true
	}
	return false
}

// rewriteZip 重写 zip：对每个条目决定删除/重命名/保留，并可追加新条目。
func rewriteZip(arc string, add []arcEntry, remove func(string) bool, rename func(string) string, opt options) error {
	zr, err := zip.OpenReader(arc)
	if err != nil {
		return fmt.Errorf("打开 zip 失败: %w", err)
	}
	tmp := arc + ".xunzip-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		zr.Close()
		return err
	}
	zw := zip.NewWriter(out)
	fail := func(e error) error {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return e
	}
	for _, f := range zr.File {
		name := f.Name
		if remove != nil && remove(name) {
			if !opt.quiet {
				fmt.Printf("d %s\n", name)
			}
			continue
		}
		if rename != nil {
			name = rename(name)
		}
		rc, err := f.Open()
		if err != nil {
			return fail(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fail(err)
		}
		hdr := f.FileHeader
		hdr.Name = name
		w, err := zw.CreateHeader(&hdr)
		if err != nil {
			return fail(err)
		}
		if _, err := w.Write(data); err != nil {
			return fail(err)
		}
		if rename != nil && name != f.Name && !opt.quiet {
			fmt.Printf("rn %s -> %s\n", f.Name, name)
		}
	}
	// 追加新条目。
	for _, e := range add {
		hdr, err := zip.FileInfoHeader(e.info)
		if err != nil {
			return fail(err)
		}
		hdr.Name = e.arcPath
		if e.isDir {
			if !strings.HasSuffix(hdr.Name, "/") {
				hdr.Name += "/"
			}
			hdr.Method = zip.Store
			if _, err := zw.CreateHeader(hdr); err != nil {
				return fail(err)
			}
			continue
		}
		hdr.Method = zipMethod(opt)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return fail(err)
		}
		if e.isLink {
			_, err = io.WriteString(w, e.link)
		} else {
			var in *os.File
			in, err = os.Open(e.fsPath)
			if err == nil {
				_, err = io.Copy(w, in)
				in.Close()
			}
		}
		if err != nil {
			return fail(err)
		}
		if !opt.quiet {
			fmt.Printf("a %s\n", e.arcPath)
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	zr.Close()
	return replaceArchive(arc, tmp)
}

// rewriteTar 重写 tar / tar.*：先解压到内存条目，再按规则写回。
func rewriteTar(arc string, f format, add []arcEntry, remove func(string) bool, rename func(string) string, opt options) error {
	in, err := os.Open(arc)
	if err != nil {
		return err
	}
	r, cleanup, err := openTarStream(in, f)
	if err != nil {
		in.Close()
		return err
	}
	type item struct {
		hdr  tar.Header
		data []byte
	}
	var items []item
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			in.Close()
			return err
		}
		if remove != nil && remove(hdr.Name) {
			if !opt.quiet {
				fmt.Printf("d %s\n", hdr.Name)
			}
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			in.Close()
			return err
		}
		h := *hdr
		if rename != nil {
			h.Name = rename(h.Name)
		}
		items = append(items, item{h, data})
	}
	if cleanup != nil {
		cleanup()
	}
	in.Close()

	tmp := arc + ".xunzip-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	w, finish, err := openTarWriter(out, f, opt)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	tw := tar.NewWriter(w)
	for _, it := range items {
		if err := tw.WriteHeader(&it.hdr); err != nil {
			tw.Close()
			if finish != nil {
				finish()
			}
			out.Close()
			os.Remove(tmp)
			return err
		}
		if _, err := tw.Write(it.data); err != nil {
			tw.Close()
			if finish != nil {
				finish()
			}
			out.Close()
			os.Remove(tmp)
			return err
		}
	}
	// 追加新条目。
	for _, e := range add {
		hdr := &tar.Header{Name: e.arcPath, ModTime: e.info.ModTime(), Format: tar.FormatPAX}
		switch {
		case e.isDir:
			hdr.Name = strings.TrimSuffix(e.arcPath, "/") + "/"
			hdr.Typeflag, hdr.Mode = tar.TypeDir, int64(dirPerm(e.info.Mode().Perm()))
		case e.isLink:
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, e.link
			hdr.Mode = int64(filePerm(e.info.Mode().Perm()))
		default:
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeReg, int64(filePerm(e.info.Mode().Perm())), e.info.Size()
			data, err := os.ReadFile(e.fsPath)
			if err != nil {
				tw.Close()
				out.Close()
				os.Remove(tmp)
				return err
			}
			if err := tw.WriteHeader(hdr); err != nil {
				tw.Close()
				out.Close()
				os.Remove(tmp)
				return err
			}
			if _, err := tw.Write(data); err != nil {
				tw.Close()
				out.Close()
				os.Remove(tmp)
				return err
			}
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			tw.Close()
			out.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := tw.Close(); err != nil {
		if finish != nil {
			finish()
		}
		out.Close()
		os.Remove(tmp)
		return err
	}
	if finish != nil {
		if err := finish(); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return replaceArchive(arc, tmp)
}

func replaceArchive(arc, tmp string) error {
	// 备份原文件以便失败时恢复。
	bak := arc + ".xunzip-bak"
	if err := os.Rename(arc, bak); err != nil {
		return err
	}
	if err := os.Rename(tmp, arc); err != nil {
		_ = os.Rename(bak, arc)
		return err
	}
	_ = os.Remove(bak)
	return nil
}

// openTarWriter 返回 tar 写入流与压缩层 finish。
func openTarWriter(out *os.File, f format, opt options) (io.Writer, func() error, error) {
	switch f {
	case fmtTar:
		return out, nil, nil
	case fmtTarGz:
		gw, err := newGzipWriter(out, opt)
		return gw, gw.Close, err
	case fmtTarBz2:
		return openTarBzip2Writer(out, opt)
	case fmtTarXz:
		xw, err := newXzWriter(out, opt)
		return xw, xw.Close, err
	case fmtTarZst:
		zw, err := newZstdWriter(out, opt)
		return zw, zw.Close, err
	}
	return nil, nil, fmt.Errorf("不支持的 tar 格式 %s", formatName(f))
}

func openTarBzip2Writer(out *os.File, opt options) (io.Writer, func() error, error) {
	bw, err := newBzip2WriterForTar(out, opt)
	if err != nil {
		return nil, nil, err
	}
	return bw, bw.Close, nil
}

// matchPattern 简单通配符匹配：支持 * 与 ?，基于 filepath.Match。
func matchPattern(pattern, name string) bool {
	ok, err := filepath.Match(pattern, filepath.Base(name))
	if err != nil {
		ok, _ = filepath.Match(pattern, name)
	}
	return ok
}
