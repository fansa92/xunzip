package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveTarget 在写文件前处理"目标已存在"的情况，对齐 7-Zip -ao 四种模式。
// 返回实际要写入的路径；若返回 os.ErrExist 表示应跳过。
// renameOld 模式下会先把已存在文件挪到后备路径。
func resolveTarget(target string, opt options) (string, error) {
	if _, err := os.Lstat(target); err != nil {
		if os.IsNotExist(err) {
			return target, nil
		}
		return "", err
	}
	switch opt.overwriteMode() {
	case owAll, owRenameOld:
		// owRenameOld: 把已存在的挪开，新内容仍写回 target。
		if opt.overwriteMode() == owRenameOld {
			_, old, renamed := renameOnExist(target, owRenameOld)
			if old != "" {
				if err := os.Rename(old, renamed); err != nil {
					return "", fmt.Errorf("重命名已存在文件失败 %q: %w", old, err)
				}
			}
		}
		return target, nil
	case owSkip:
		return "", os.ErrExist
	case owRenameNew:
		newPath, _, _ := renameOnExist(target, owRenameNew)
		return newPath, nil
	}
	return target, nil
}

// skipMessage 返回"跳过已存在"的提示文案。
func skipMessage(target string, opt options) string {
	if !opt.quiet {
		return fmt.Sprintf("跳过已存在: %s（-ao s 跳过 / -f 覆盖）", target)
	}
	return ""
}

// baseNameNoExt 返回去掉扩展名后的文件名，供分卷与重命名使用。
func baseNameNoExt(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}
