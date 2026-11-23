package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// options 是全局选项，覆盖解压/压缩/列出/测试/包内修改等所有模式。
// 零值表达"用户未指定"，因此新字段都可以安全加到末尾。
type options struct {
	// ---- 基础（旧版已有）----
	out      string // -o 输出目录，空表示当前目录
	listOnly bool   // -l 只列出
	testOnly bool   // -t 只测试
	force    bool   // -f 覆盖已存在
	quiet    bool   // -q 静默
	strip    int    // --strip N
	password string // -p 密码

	// ---- P0 压缩参数 ----
	level     int    // -level 0..9，-1 表示未指定
	method    string // -method store|deflate|bzip2|xz|zstd|lzma2|copy
	threads   int    // -threads N，0 表示自动
	dict      string // -dict 32m 等
	solid     bool   // -solid（7z/tar 顺序打包，目前仅记录）
	overwrite string // -ao a|s|t|u
	volumes   string // -v SIZE（如 10m）
	stdin     bool   // -si 从 stdin 读
	stdout    bool   // -so 写到 stdout

	// ---- P1 ----
	forceType string   // -type 强制指定格式
	tech      bool     // -slt 技术详情
	json      bool     // --json 机器可读输出
	flat      bool     // -e 平铺解压（忽略包内路径）
	include   []string // -i 包含模式
	exclude   []string // -x 排除模式
	listfile  string   // @listfile 清单文件
	hash      string   // h 命令哈希算法
	sdel      bool     // -sdel 压缩后删除源文件
	stl       bool     // -stl 包时间戳取最新文件
	sni       bool     // -sni 记录 NTFS 安全信息（尽力而为）
	snh       bool     // -snh 硬链接存为链接
	snl       bool     // -snl 符号链接存为链接
	logLevel  int      // -bb 0..3
	showTime  bool     // -bt 显示耗时
	workDir   string   // -w 工作目录
	yes       bool     // -y 所有询问回答是

	// ---- P2 ----
	motw     bool // -motw 写入 Zone.Identifier
	benchSec int  // b 命令基准时长
	info     bool // i 命令格式信息
}

// 覆盖模式取值，对齐 7-Zip 的 -ao。
const (
	owNone      = ""
	owAll       = "a" // 覆盖已存在
	owSkip      = "s" // 跳过已存在（默认）
	owRenameOld = "t" // 自动重命名已存在的文件
	owRenameNew = "u" // 自动重命名新解出的文件
)

// overwriteMode 返回最终的覆盖模式：-ao 优先，兼容旧版 -f。
func (o options) overwriteMode() string {
	if o.overwrite != owNone {
		return strings.ToLower(o.overwrite)
	}
	if o.force {
		return owAll
	}
	return owSkip
}

// skipExisting 是否应跳过已存在文件。
func (o options) skipExisting() bool { return o.overwriteMode() == owSkip }

// validOverwrite 校验 -ao 参数。
func validOverwrite(v string) bool {
	switch strings.ToLower(v) {
	case owAll, owSkip, owRenameOld, owRenameNew:
		return true
	}
	return false
}

// renameOnExist 为已存在的目标生成一个不冲突的新路径。
// mode=t 时重命名已存在文件（先挪开旧的），mode=u 时给新文件加后缀。
// 返回 (实际写入路径, 需要对旧文件做的改名源, 需要对旧文件做的改名目标)。
func renameOnExist(target, mode string) (string, string, string) {
	if mode == owRenameNew {
		ext := filepath.Ext(target)
		stem := strings.TrimSuffix(target, ext)
		for i := 1; ; i++ {
			cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
			if _, err := os.Stat(cand); err != nil {
				return cand, "", ""
			}
		}
	}
	if mode == owRenameOld {
		ext := filepath.Ext(target)
		stem := strings.TrimSuffix(target, ext)
		for i := 1; ; i++ {
			cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
			if _, err := os.Stat(cand); err != nil {
				return target, target, cand
			}
		}
	}
	return target, "", ""
}
