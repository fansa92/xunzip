// xunzip 解压缩命令行工具，兼容旧式标志用法与 7z 风格命令。
//
// 示例：
//
//	xunzip a.zip                         解压到当前目录
//	xunzip x -o ./out a.zip b.tar.gz     解压到 ./out
//	xunzip l a.zip                       列出内容
//	xunzip t a.7z                        测试完整性
//	xunzip a backup.zip docs/ a.txt      压缩
//	xunzip a -p secret secret.zip docs/  创建 AES-256 加密 zip
package main

import (
	"fmt"
	"io"
	"os"
	"time"
)

// version 由 CI 通过 -ldflags "-X main.version=..." 注入；
// 必须是 var 而非 const，否则链接器无法覆盖。
var version = "1.3.1"

func main() {
	cp := setupConsoleUTF8()
	code := run(os.Args[1:])
	cp.restore()
	os.Exit(code)
}

func run(argv []string) int {
	start := time.Now()
	p := parseCLI(argv)
	// -w 工作目录。
	if p.opt.workDir != "" {
		if err := os.Chdir(p.opt.workDir); err != nil {
			fmt.Fprintln(os.Stderr, "错误: 切换工作目录失败:", err)
			return 2
		}
	}
	// -bb 0 静默；-bb 3 详细（覆盖 -q）；-1 表示未指定。
	if p.opt.logLevel == 0 {
		p.opt.quiet = true
	}
	if p.opt.logLevel >= 3 {
		p.opt.quiet = false
	}
	defer func() {
		if p.opt.showTime {
			fmt.Fprintf(os.Stderr, "\n耗时: %s\n", time.Since(start).Round(time.Millisecond))
		}
	}()
	if p.badArg != "" {
		fmt.Fprintln(os.Stderr, "错误:", p.badArg)
		usage(os.Stderr)
		return 7 // 命令行错误，对齐 7-Zip
	}
	if p.ver {
		fmt.Printf("xunzip %s\n", version)
		return 0
	}
	if p.help {
		usage(os.Stdout)
		return 0
	}
	if len(p.args) == 0 && p.cmd != cmdInfo && p.cmd != cmdBench {
		usage(os.Stderr)
		return 7
	}

	switch p.cmd {
	case cmdAdd:
		return runAdd(p)
	case cmdExtract, cmdExtractX:
		return runExtract(p)
	case cmdList:
		return runList(p)
	case cmdTest:
		return runTest(p)
	case cmdDelete:
		return runDelete(p)
	case cmdUpdate:
		return runUpdate(p)
	case cmdRename:
		return runRename(p)
	case cmdHash:
		return runHash(p)
	case cmdInfo:
		return runInfo(p)
	case cmdBench:
		return runBench(p)
	default:
		fmt.Fprintln(os.Stderr, "错误: 未知命令")
		usage(os.Stderr)
		return 7
	}
}

// aggregateResult 把批量处理的错误汇总成退出码：
// 0 正常 / 1 有跳过或警告 / 2 致命错误。
func aggregateResult(anySkip bool, errs []error) int {
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "失败:", e)
		}
		return 2
	}
	if anySkip {
		return 1
	}
	return 0
}

// runExtract 解压（x=保留路径，e=平铺）。
func runExtract(p parsedCLI) int {
	var errs []error
	anySkip := false
	// -si：从 stdin 读入临时文件作为输入。
	if p.opt.stdin {
		tmp, cleanup, err := stdinToTemp()
		if err != nil {
			fmt.Fprintln(os.Stderr, "失败: 读取 stdin:", err)
			return 2
		}
		defer cleanup()
		p.args = []string{tmp}
	}
	for _, arc := range p.args {
		opt := p.opt
		if p.cmd == cmdExtract {
			opt.flat = true
		}
		// 分卷：把 .001/.002... 合并成临时文件。
		realArc, cleanup, jerr := joinVolumes(arc)
		if jerr != nil {
			errs = append(errs, fmt.Errorf("[%s] 合并分卷失败: %w", arc, jerr))
			continue
		}
		if cleanup != nil {
			defer cleanup()
		}
		if !opt.quiet && !opt.stdout {
			fmt.Printf("解压: %s [%s]\n", arc, formatName(detectFormat(arc)))
		}
		st, err := processArchive(realArc, opt)
		if err != nil {
			errs = append(errs, fmt.Errorf("[%s] %w", arc, err))
			continue
		}
		if st.skip > 0 {
			anySkip = true
		}
		if !opt.quiet && !opt.stdout {
			fmt.Printf("完成: %s\n", arc)
		}
	}
	return aggregateResult(anySkip, errs)
}

// runList 列出内容。
func runList(p parsedCLI) int {
	if p.opt.tech || p.opt.json {
		return runListDetailed(p)
	}
	var errs []error
	for _, arc := range p.args {
		realArc, cleanup, jerr := joinVolumes(arc)
		if jerr != nil {
			errs = append(errs, fmt.Errorf("[%s] 合并分卷失败: %w", arc, jerr))
			continue
		}
		if cleanup != nil {
			defer cleanup()
		}
		f := resolveFormat(realArc, p.opt)
		if err := listArchive(realArc, f, p.opt); err != nil {
			errs = append(errs, fmt.Errorf("[%s] %w", arc, err))
		}
	}
	return aggregateResult(false, errs)
}

// runTest 测试完整性。
func runTest(p parsedCLI) int {
	var errs []error
	anySkip := false
	for _, arc := range p.args {
		opt := p.opt
		opt.testOnly = true
		opt.listOnly = false
		realArc, cleanup, jerr := joinVolumes(arc)
		if jerr != nil {
			errs = append(errs, fmt.Errorf("[%s] 合并分卷失败: %w", arc, jerr))
			continue
		}
		if cleanup != nil {
			defer cleanup()
		}
		st, err := processArchive(realArc, opt)
		if err != nil {
			errs = append(errs, fmt.Errorf("[%s] %w", arc, err))
			continue
		}
		if st.skip > 0 {
			anySkip = true
		}
	}
	return aggregateResult(anySkip, errs)
}

// runAdd 压缩：xunzip a out.zip inputs...（也兼容旧式 -c out.zip inputs...）。
func runAdd(p parsedCLI) int {
	opt := p.opt
	if opt.stdin {
		if len(p.args) < 1 {
			fmt.Fprintln(os.Stderr, "错误: -si 需要指定输出包")
			return 7
		}
		tmp, cleanup, err := stdinToTemp()
		if err != nil {
			fmt.Fprintln(os.Stderr, "失败: 读取 stdin:", err)
			return 2
		}
		defer cleanup()
		compressArchive(p.args[0], []string{tmp}, opt)
		return 0
	}
	if len(p.args) < 2 {
		fmt.Fprintln(os.Stderr, "错误: a 命令需要指定输出包和至少一个输入")
		return 7
	}
	outArc := p.args[0]
	inputs := p.args[1:]
	if _, err := compressArchive(outArc, inputs, opt); err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		return 2
	}
	if opt.sdel {
		for _, in := range inputs {
			if err := os.RemoveAll(in); err != nil {
				fmt.Fprintln(os.Stderr, "警告: 删除源文件失败:", err)
			}
		}
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `xunzip %s - 解压缩工具（7z 风格命令 + 旧式标志）

用法:
  xunzip x [选项] <压缩包>...          解压（保留目录结构）
  xunzip e [选项] <压缩包>...          平铺解压（忽略目录结构）
  xunzip l [选项] <压缩包>...          列出内容
  xunzip t [选项] <压缩包>...          测试完整性（不落盘）
  xunzip a [选项] <输出包> <文件/目录>...  压缩
  xunzip d [选项] <压缩包> <条目>...   删除包内条目（zip/tar）
  xunzip u [选项] <压缩包> <文件>...   更新包内条目（zip/tar）
  xunzip rn <压缩包> <旧名> <新名>     重命名包内条目（zip/tar）
  xunzip h [选项] <文件>...            计算哈希（CRC32/CRC64/SHA1/SHA256）
  xunzip i                             显示支持的格式
  xunzip b [选项] <压缩包>             压缩/解压基准测试

旧式兼容:
  xunzip <压缩包>...                   默认解压
  xunzip -l <压缩包>                   列出
  xunzip -t <压缩包>                   测试
  xunzip -c <输出包> <文件/目录>...    压缩

主要选项:
  -o, -output <目录>   输出目录
  -p, -password <密码> 密码（zip/rar/7z 解压；创建加密 zip）
  -f, -force           覆盖（等价于 -ao a）
  -ao {a|s|t|u}        覆盖模式：覆盖/跳过/重命名旧/重命名新
  -q, -quiet           静默
  -strip N             去掉包内路径前 N 级
  -type <格式>         强制指定格式
  -level 0-9, -mx N    压缩级别（7z: 0=Store，1-9 调 LZMA2 字典）
  -method <方法>       zip: store|deflate；7z: store|lzma2
  -threads N           线程数（gzip 并行 / zstd 并发；7z 单线程）
  -dict <大小>         xz / 7z 字典大小 如 32m
  -solid               solid 打包（zip/tar 顺序；7z 写入不支持）
  -v <大小>            分卷大小 如 10m
  -si / -so            stdin 输入 / stdout 输出
  -slt / --json        技术详情 / JSON 输出
  -i/-x <模式>         包含/排除（逗号分隔，支持 *）
  -listfile <文件>     从清单文件读取参数
  -scrc <算法>         h 命令哈希算法
  -sdel / -stl         压缩后删源 / 用最新文件时间
  -bb 0-3 / -bt        日志级别 / 显示耗时
  -w <目录>            工作目录
  -y                   全部确认
  -motw                写入 Zone.Identifier
  -version / -h        版本 / 帮助

支持格式:
  解压: zip(AES/ZipCrypto) rar rar5 7z tar tar.gz/.tgz tar.bz2 tar.xz
        tar.zst gz bz2 xz zst lz4 br lz lzma（单文件流）
  压缩: zip(AES-256) 7z(Store/LZMA2) tar tar.gz tar.bz2 tar.xz tar.zst
        gz bz2 xz zst lz4 br lz lzma
  注: 不创建 rar（专有格式）；7z 写入不支持加密、solid 块、修改时间与符号链接类型。

安全:
  自动拦截绝对路径与 ../ 跳出输出目录的条目；symlink 目标校验。

示例:
  xunzip x -o ./out a.zip b.tar.gz
  xunzip a -level 9 -threads 4 backup.tar.gz docs/
  xunzip a -mx9 backup.7z docs/
  xunzip a -p secret secret.zip docs/
  xunzip h -scrc SHA256 a.zip
  xunzip -c -p secret secret.zip docs/
`, version)
}
