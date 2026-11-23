package main

import (
	"archive/tar"
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

// entryInfo 是 -slt / --json 统一输出的条目信息。
type entryInfo struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Compressed int64  `json:"compressed,omitempty"`
	Modified   string `json:"modified,omitempty"`
	Method     string `json:"method,omitempty"`
	Encrypted  bool   `json:"encrypted,omitempty"`
	IsDir      bool   `json:"is_dir,omitempty"`
	IsSymlink  bool   `json:"is_symlink,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
	CRC32      string `json:"crc32,omitempty"`
	Format     string `json:"format"`
}

// detailedList 收集压缩包内所有条目的详细信息。
func detailedList(path string, f format, opt options) ([]entryInfo, error) {
	var out []entryInfo
	switch f {
	case fmtZip:
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, zf := range zr.File {
			e := entryInfo{
				Name: zf.Name, Size: int64(zf.UncompressedSize64),
				Compressed: int64(zf.CompressedSize64), Modified: zf.Modified.Format("2006-01-02 15:04:05"),
				Method: fmt.Sprintf("%d", zf.Method), Encrypted: zf.Flags&0x1 != 0,
				IsDir: zf.FileInfo().IsDir(), CRC32: fmt.Sprintf("%08x", zf.CRC32), Format: "zip",
			}
			if zf.Mode()&os.ModeSymlink != 0 {
				e.IsSymlink = true
				if rc, err := zf.Open(); err == nil {
					b, _ := io.ReadAll(io.LimitReader(rc, 4096))
					rc.Close()
					e.LinkTarget = strings.TrimSpace(string(b))
				}
			}
			out = append(out, e)
		}
	case fmtTar, fmtTarGz, fmtTarBz2, fmtTarXz, fmtTarZst:
		in, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer in.Close()
		r, cleanup, err := openTarStream(in, f)
		if err != nil {
			return nil, err
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
				return nil, err
			}
			e := entryInfo{
				Name: hdr.Name, Size: hdr.Size, Modified: hdr.ModTime.Format("2006-01-02 15:04:05"),
				Method: "tar", IsDir: hdr.Typeflag == tar.TypeDir, Format: formatName(f),
			}
			if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
				e.IsSymlink = true
				e.LinkTarget = hdr.Linkname
			}
			out = append(out, e)
			_, _ = io.Copy(io.Discard, tr)
		}
	case fmtRar:
		files, err := rardecode.List(path, rarOpenOpts(opt)...)
		if err != nil {
			return nil, err
		}
		for _, rf := range files {
			out = append(out, entryInfo{
				Name: rf.Name, Size: rf.UnPackedSize, Modified: rf.ModificationTime.Format("2006-01-02 15:04:05"),
				IsDir: rf.IsDir, Encrypted: rf.Encrypted, Format: "rar",
			})
		}
	case fmtSevenZ:
		zr, err := sevenzip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, sf := range zr.File {
			out = append(out, entryInfo{
				Name: sf.Name, Size: int64(sf.UncompressedSize),
				Modified: sf.Modified.Format("2006-01-02 15:04:05"),
				IsDir:    sf.FileInfo().IsDir(), Format: "7z",
			})
		}
	default:
		return nil, fmt.Errorf("该格式不支持详细列表")
	}
	return out, nil
}

// runListDetailed 处理 -slt / --json 的列表输出。
func runListDetailed(p parsedCLI) int {
	var errs []error
	for _, arc := range p.args {
		realArc, cleanup, jerr := joinVolumes(arc)
		if jerr != nil {
			errs = append(errs, jerr)
			continue
		}
		if cleanup != nil {
			defer cleanup()
		}
		f := resolveFormat(realArc, p.opt)
		entries, err := detailedList(realArc, f, p.opt)
		if err != nil {
			errs = append(errs, fmt.Errorf("[%s] %w", arc, err))
			continue
		}
		if p.opt.json {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(entries); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		for _, e := range entries {
			fmt.Printf("Path = %s\n", e.Name)
			fmt.Printf("Size = %d\n", e.Size)
			fmt.Printf("Modified = %s\n", e.Modified)
			if e.Compressed > 0 {
				fmt.Printf("Packed Size = %d\n", e.Compressed)
			}
			if e.Method != "" {
				fmt.Printf("Method = %s\n", e.Method)
			}
			if e.CRC32 != "" {
				fmt.Printf("CRC = %s\n", e.CRC32)
			}
			if e.Encrypted {
				fmt.Println("Encrypted = +")
			}
			if e.IsDir {
				fmt.Println("Folder = +")
			}
			if e.IsSymlink {
				fmt.Printf("Symlink = %s\n", e.LinkTarget)
			}
			fmt.Println()
		}
	}
	return aggregateResult(false, errs)
}
