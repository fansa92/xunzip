package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// 7z 风格的单字母命令。
type command string

const (
	cmdAdd      command = "a"  // 添加/压缩
	cmdDelete   command = "d"  // 删除条目
	cmdExtract  command = "e"  // 平铺解压
	cmdHash     command = "h"  // 计算哈希
	cmdInfo     command = "i"  // 格式信息
	cmdList     command = "l"  // 列出
	cmdRename   command = "rn" // 重命名条目
	cmdTest     command = "t"  // 测试
	cmdUpdate   command = "u"  // 更新
	cmdExtractX command = "x"  // 带路径解压
	cmdBench    command = "b"  // 基准测试
)

var commandSet = map[string]command{
	"a": cmdAdd, "d": cmdDelete, "e": cmdExtract, "h": cmdHash,
	"i": cmdInfo, "l": cmdList, "rn": cmdRename, "t": cmdTest,
	"u": cmdUpdate, "x": cmdExtractX, "b": cmdBench,
}

// parsedCLI 是解析后的命令行。
type parsedCLI struct {
	cmd    command // 命令（旧式用法时按标志推导出 x/l/t/a）
	opt    options
	args   []string // 位置参数（压缩包、输入文件等）
	ver    bool
	help   bool
	badArg string // 非法参数说明
}

// parseCLI 解析参数，支持两种风格：
//  1. 7z 风格：xunzip <命令> [选项] <参数...>
//  2. 旧式： xunzip [选项] <压缩包...>  （默认 x；-l -> l；-t -> t；-c -> a）
func parseCLI(argv []string) parsedCLI {
	var p parsedCLI
	fs := flag.NewFlagSet("xunzip", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var out, output string
	var list, listLong, test, testLong bool
	var force, forceLong, quiet, quietLong, ver, help, helpLong bool
	var strip int
	var password, passwordLong string
	var compress, compressLong string
	var level int
	var method, dict, overwrite, volumes string
	var threads int
	var solid, si, so bool
	var forceType, listfile, hash, workdir string
	var tech, json, flat, sdel, stl, sni, snh, snl, showTime, yes, motw bool
	var logLevel int
	var benchSec int
	var incStr, excStr string

	fs.StringVar(&out, "o", "", "输出目录")
	fs.StringVar(&output, "output", "", "输出目录")
	fs.BoolVar(&list, "l", false, "列出")
	fs.BoolVar(&listLong, "list", false, "列出")
	fs.BoolVar(&test, "t", false, "测试")
	fs.BoolVar(&testLong, "test", false, "测试")
	fs.BoolVar(&force, "f", false, "覆盖")
	fs.BoolVar(&forceLong, "force", false, "覆盖")
	fs.BoolVar(&quiet, "q", false, "静默")
	fs.BoolVar(&quietLong, "quiet", false, "静默")
	fs.IntVar(&strip, "strip", 0, "剥离路径层级")
	fs.StringVar(&password, "p", "", "密码")
	fs.StringVar(&passwordLong, "password", "", "密码")
	fs.StringVar(&compress, "c", "", "压缩输出包")
	fs.StringVar(&compressLong, "compress", "", "压缩输出包")
	fs.IntVar(&level, "level", -1, "压缩级别 0-9")
	fs.IntVar(&level, "mx", -1, "压缩级别 0-9（7-Zip 别名）")
	fs.StringVar(&method, "method", "", "压缩方法 store|deflate|...")
	fs.IntVar(&threads, "threads", 0, "线程数")
	fs.StringVar(&dict, "dict", "", "字典大小 如 32m")
	fs.BoolVar(&solid, "solid", false, "solid 打包")
	fs.StringVar(&overwrite, "ao", "", "覆盖模式 a|s|t|u")
	fs.StringVar(&volumes, "v", "", "分卷大小 如 10m")
	fs.BoolVar(&si, "si", false, "从 stdin 读")
	fs.BoolVar(&so, "so", false, "写到 stdout")
	fs.StringVar(&forceType, "type", "", "强制格式")
	fs.BoolVar(&tech, "slt", false, "技术详情")
	fs.BoolVar(&json, "json", false, "JSON 输出")
	fs.BoolVar(&flat, "e", false, "平铺解压")
	fs.StringVar(&incStr, "i", "", "包含模式")
	fs.StringVar(&excStr, "x", "", "排除模式")
	fs.StringVar(&listfile, "listfile", "", "清单文件")
	fs.StringVar(&hash, "scrc", "SHA256", "哈希算法")
	fs.BoolVar(&sdel, "sdel", false, "压缩后删除源文件")
	fs.BoolVar(&stl, "stl", false, "用最新文件时间设包时间")
	fs.BoolVar(&sni, "sni", false, "记录安全信息")
	fs.BoolVar(&snh, "snh", false, "硬链接存为链接")
	fs.BoolVar(&snl, "snl", false, "符号链接存为链接")
	fs.IntVar(&logLevel, "bb", -1, "日志级别 0-3")
	fs.BoolVar(&showTime, "bt", false, "显示耗时")
	fs.StringVar(&workdir, "w", "", "工作目录")
	fs.BoolVar(&yes, "y", false, "全部确认")
	fs.BoolVar(&motw, "motw", false, "写入 Zone.Identifier")
	fs.IntVar(&benchSec, "benchtime", 5, "基准时长(秒)")
	fs.BoolVar(&ver, "version", false, "版本")
	fs.BoolVar(&help, "h", false, "帮助")
	fs.BoolVar(&helpLong, "help", false, "帮助")

	// 检测 7z 风格命令：第一个非选项 token 若命中命令集，则视为命令。
	args := argv
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if c, ok := commandSet[args[0]]; ok {
			p.cmd = c
			args = args[1:]
		}
	}

	args = splitAttachedValues(fs, args)
	args = permute(fs, args)
	if err := fs.Parse(args); err != nil {
		p.badArg = "参数解析失败"
		return p
	}

	p.ver = ver
	p.help = help || helpLong
	p.opt = options{
		out:       strings.TrimSpace(firstNonEmpty(out, output)),
		listOnly:  list || listLong,
		testOnly:  test || testLong,
		force:     force || forceLong,
		quiet:     quiet || quietLong,
		strip:     strip,
		password:  firstNonEmpty(password, passwordLong),
		level:     level,
		method:    method,
		threads:   threads,
		dict:      dict,
		solid:     solid,
		overwrite: overwrite,
		volumes:   volumes,
		stdin:     si,
		stdout:    so,
		forceType: forceType,
		tech:      tech,
		json:      json,
		flat:      flat,
		listfile:  listfile,
		hash:      hash,
		sdel:      sdel,
		stl:       stl,
		sni:       sni,
		snh:       snh,
		snl:       snl,
		logLevel:  logLevel,
		showTime:  showTime,
		workDir:   workdir,
		yes:       yes,
		motw:      motw,
		benchSec:  benchSec,
	}
	if incStr != "" {
		p.opt.include = strings.Split(incStr, ",")
	}
	if excStr != "" {
		p.opt.exclude = strings.Split(excStr, ",")
	}
	// @listfile：从文件读入参数列表（每行一个）。
	if listfile != "" {
		lines, err := readListFile(listfile)
		if err != nil {
			p.badArg = err.Error()
			return p
		}
		p.args = lines
	} else {
		p.args = fs.Args()
	}

	// 压缩包参数：旧式 -c out.zip 表示把 out.zip 作为输出，其余是输入。
	arc := strings.TrimSpace(firstNonEmpty(compress, compressLong))
	if arc != "" {
		p.cmd = cmdAdd
		p.opt.out = arc // 复用 out 字段存输出包路径
		p.args = append([]string{arc}, p.args...)
	}

	// 旧式标志推导出命令。
	if p.cmd == "" {
		switch {
		case p.opt.listOnly:
			p.cmd = cmdList
		case p.opt.testOnly:
			p.cmd = cmdTest
		default:
			p.cmd = cmdExtractX
		}
	}
	// -e 平铺：旧式 -e 即 e 命令。
	if flat && p.cmd == cmdExtractX {
		p.cmd = cmdExtract
	}

	if strip < 0 {
		p.badArg = "--strip 不能为负数"
	}
	if level < -1 || level > 9 {
		p.badArg = "-level 必须在 0-9 之间"
	}
	if overwrite != "" && !validOverwrite(overwrite) {
		p.badArg = "-ao 必须是 a|s|t|u"
	}
	return p
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// readListFile 读取 @listfile：每行一个路径，忽略空行和 # 注释。
func readListFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取清单文件失败: %w", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// 允许紧贴写法的值型选项名，按长度降序匹配。
// 必须降序：-mx9 要先试 "mx" 再试 "m"，否则会被拆成 -m + x9。
var attachedValueNames = []string{
	"mx", "o", "p", "v", "w", "c", "i", "x",
}

// splitAttachedValues 把 -oDIR / -mx9 这类紧贴写法拆成 -o DIR / -mx 9。
//
// 判定分三步，缺一步就会拆坏别的选项：
//  1. 含 '=' 的原样放行（flag 包自己能处理 -o=DIR）；
//  2. 整词能被 FlagSet 认识的原样放行（否则 -output 会被拆成 -o utput）；
//  3. 只有值型选项名才拆（-so / -slt / -sdel 等布尔项因此不受影响）。
func splitAttachedValues(fs *flag.FlagSet, args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) < 3 || a[0] != '-' || a[1] == '-' || strings.ContainsRune(a, '=') {
			out = append(out, a)
			continue
		}
		if fs.Lookup(a[1:]) != nil {
			out = append(out, a)
			continue
		}
		split := false
		for _, name := range attachedValueNames {
			prefix := "-" + name
			if len(a) > len(prefix) && strings.HasPrefix(a, prefix) {
				out = append(out, prefix, a[len(prefix):])
				split = true
				break
			}
		}
		if !split {
			out = append(out, a)
		}
	}
	return out
}

// boolFlags 供 permute 判断哪些选项不带值。
var boolFlags = map[string]bool{
	"h": true, "help": true, "version": true,
	"l": true, "list": true, "t": true, "test": true,
	"f": true, "force": true, "q": true, "quiet": true,
	"solid": true, "si": true, "so": true,
	"slt": true, "json": true, "e": true,
	"sdel": true, "stl": true, "sni": true, "snh": true, "snl": true,
	"bt": true, "y": true, "motw": true,
}

// permute 把选项移到位置参数前面，允许 `xunzip a.zip -o out` 这种写法。
func permute(fs *flag.FlagSet, args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if idx := strings.IndexByte(name, '='); idx >= 0 {
				continue
			}
			if !boolFlags[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}
