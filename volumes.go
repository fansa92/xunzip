package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// splitVolumes 把已生成的压缩包按 size 切成多个分卷：
// name.001 / name.002 / ...（保留原包，调用方可选择删除）。
func splitVolumes(path, spec string) error {
	size, err := parseSize(spec)
	if err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	buf := make([]byte, 1<<20)
	part := 1
	for {
		partPath := fmt.Sprintf("%s.%03d", path, part)
		out, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		remaining := size
		for remaining > 0 {
			toRead := int64(len(buf))
			if remaining < toRead {
				toRead = remaining
			}
			n, err := in.Read(buf[:toRead])
			if n > 0 {
				if _, werr := out.Write(buf[:n]); werr != nil {
					out.Close()
					return werr
				}
				remaining -= int64(n)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				out.Close()
				return err
			}
		}
		if err := out.Close(); err != nil {
			return err
		}
		// 判断是否已读完：peek 下一个字节；有则回退供下一卷读取。
		one := make([]byte, 1)
		n, err := in.Read(one)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if n > 0 {
			if _, err := in.Seek(-1, io.SeekCurrent); err != nil {
				return err
			}
			part++
			continue
		}
		break
	}
	return nil
}

// joinVolumes 检测并合并分卷：若 path 存在或存在 path.001/.002...，
// 返回合并后的临时文件路径与清理函数；否则返回原路径。
func joinVolumes(path string) (string, func(), error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	// 若传入的是 .NNN 分卷文件名，去掉序号得到基名。
	if len(base) > 4 {
		suffix := base[len(base)-4:]
		if suffix[0] == '.' && allDigits(suffix[1:]) {
			base = base[:len(base)-4]
		}
	}
	if _, err := os.Stat(filepath.Join(dir, base)); err == nil {
		return filepath.Join(dir, base), func() {}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return path, func() {}, nil
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > len(base)+3 && strings.HasPrefix(name, base+".") {
			suffix := name[len(base)+1:]
			if allDigits(suffix) {
				parts = append(parts, filepath.Join(dir, name))
			}
		}
	}
	if len(parts) == 0 {
		return path, func() {}, nil
	}
	sort.Slice(parts, func(i, j int) bool {
		si := parts[i][len(parts[i])-4+1:]
		sj := parts[j][len(parts[j])-4+1:]
		if len(si) != len(sj) {
			return len(si) < len(sj)
		}
		return si < sj
	})
	tmp, err := os.CreateTemp("", "xunzip-vol-*")
	if err != nil {
		return "", nil, err
	}
	for _, part := range parts {
		f, err := os.Open(part)
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", nil, err
		}
		_, err = io.Copy(tmp, f)
		f.Close()
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", nil, err
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", nil, err
	}
	return tmp.Name(), func() { _ = os.Remove(tmp.Name()) }, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
