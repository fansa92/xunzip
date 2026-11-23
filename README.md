# xunzip — 解压缩命令行工具（v1.3.0）

[![CI](https://github.com/fansa92/xunzip/actions/workflows/ci.yml/badge.svg)](https://github.com/fansa92/xunzip/actions/workflows/ci.yml)
[![Release](https://github.com/fansa92/xunzip/actions/workflows/release.yml/badge.svg)](https://github.com/fansa92/xunzip/actions/workflows/release.yml)

放在独立文件夹 `xunzip/` 内，与主项目的 `gdl` 互不干扰。全部为**纯 Go** 实现（无需 cgo、无外部二进制）。同时支持 7z 风格命令与旧式标志用法，对齐 7-Zip 命令行的常用命令/开关，并补齐 zip/tar/7z 打包、包内增删改、分卷、管道、哈希、详细列表等能力。

## 安装

从 [Releases](https://github.com/fansa92/xunzip/releases) 下载对应平台的压缩包，解出单个可执行文件即可使用，无运行时依赖。

已发布的目标：

| 平台 | 架构 |
| --- | --- |
| Linux | `amd64`、`arm64`、`386`、`armv7`、`armv6` |
| Windows | `amd64`、`arm64`、`386` |
| macOS | `amd64`、`arm64`（Apple Silicon） |
| FreeBSD | `amd64`、`arm64` |

压缩包同时附 `SHA256SUMS.txt`，可校验：

```bash
sha256sum -c SHA256SUMS.txt
```

## 构建

```powershell
cd xunzip
C:\Go\bin\go.exe build -o xunzip.exe .
```

> 需要 Go **1.27.4+**：7z 写入依赖 `go-filesystems/sevenzip`，该库要求 1.27.4。
> `go.mod` 已相应声明 `go 1.27.4`，低于此版本的工具链会自动走 Go 的 toolchain 下载。

## 用法（7z 风格命令）

```text
xunzip x [选项] <压缩包>...            解压（保留目录结构）
xunzip e [选项] <压缩包>...            平铺解压（忽略目录结构）
xunzip l [选项] <压缩包>...            列出内容
xunzip t [选项] <压缩包>...            测试完整性（不落盘）
xunzip a [选项] <输出包> <文件/目录>... 压缩
xunzip d [选项] <压缩包> <条目>...     删除包内条目（zip/tar）
xunzip u [选项] <压缩包> <文件>...     更新包内条目（zip/tar）
xunzip rn <压缩包> <旧名> <新名>       重命名包内条目（zip/tar）
xunzip h [选项] <文件>...              计算哈希（CRC32/CRC64/SHA1/SHA256/SHA512/MD5）
xunzip i                               显示支持的格式与实现
xunzip b [选项] <压缩包>               压缩/解压基准测试（默认 5 秒，-benchtime N 调整）
```

旧式用法保持兼容：

```text
xunzip <压缩包>...                      默认解压
xunzip -l <压缩包> / -t <压缩包>        列出 / 测试
xunzip -c <输出包> <文件/目录>...       压缩
```

## 选项

| 选项 | 说明 |
| --- | --- |
| `-o, -output <目录>` | 输出目录（默认当前目录） |
| `-p, -password <密码>` | 密码（zip/rar/7z 解压；创建加密 zip） |
| `-f, -force` | 覆盖（等价于 `-ao a`） |
| `-ao {a\|s\|t\|u}` | 覆盖模式：覆盖/跳过/重命名旧文件/重命名新文件 |
| `-q, -quiet` | 静默 |
| `-strip N` | 去掉包内路径前 N 级 |
| `-type <格式>` | 强制指定格式（跳过后缀/魔数检测） |
| `-level 0-9, -mx N` | 压缩级别（gzip/zstd/bzip2/zip deflate；7z: 0=Store，1-9 调 LZMA2 字典） |
| `-method <方法>` | zip: `store` / `deflate`；7z: `store` / `lzma2` |
| `-threads N` | 线程数（gzip 并行用 klauspost 实现，zstd 并发；7z 单线程） |
| `-dict <大小>` | xz / 7z 字典大小，如 `32m` |
| `-solid` | solid 打包（zip/tar 顺序打包；7z 写入不支持 solid 块） |
| `-v <大小>` | 分卷大小，如 `10m`；解压时传 `.001` 自动合并全部分卷 |
| `-si / -so` | 从 stdin 读 / 写到 stdout（管道组合） |
| `-slt / --json` | 技术详情列表 / JSON 输出 |
| `-i/-x <模式>` | 包含/排除模式（逗号分隔，支持 `*` 与 `**`） |
| `-listfile <文件>` | 从清单文件读取参数（每行一个，`#` 注释） |
| `-scrc <算法>` | `h` 命令的哈希算法 |
| `-sdel / -stl` | 压缩后删除源文件 / 用最新文件时间设包时间 |
| `-bb 0-3 / -bt` | 日志级别 / 显示耗时 |
| `-w <目录>` | 工作目录 |
| `-y` | 全部确认（预留） |
| `-motw` | 给解压文件写入 Zone.Identifier（仅 Windows） |
| `-version / -h` | 版本 / 帮助 |

## 支持格式（详见 `xunzip i`）

| 格式 | 解压 | 压缩 | 实现 |
| --- | --- | --- | --- |
| `.zip` | ✓ | ✓ | 标准库 + `yeka/zip`（WinZip AES + 传统 ZipCrypto） |
| `.rar` / `.rar5` | ✓ | — | `nwaples/rardecode/v2`，含 solid / 分卷 |
| `.7z` | ✓ | ✓ | 解压 `bodgit/sevenzip`（含分卷首卷 / 加密）；写入 `go-filesystems/sevenzip`（Store / LZMA2） |
| `.tar` 系列 | ✓ | ✓ | 标准库 `archive/tar` |
| `.tar.gz` / `.tgz` | ✓ | ✓ | 标准库 gzip（多线程用 `klauspost/compress/gzip`） |
| `.tar.bz2` / `.tbz2` | ✓ | ✓ | 解压标准库 / 压缩 `dsnet/compress` |
| `.tar.xz` / `.txz` | ✓ | ✓ | `ulikunitz/xz` |
| `.tar.zst` / `.tzst` | ✓ | ✓ | `klauspost/compress/zstd` |
| `.gz` / `.bz2` / `.xz` / `.zst` | ✓ | ✓ | 单文件流 |
| `.lz4` | ✓ | ✓ | `pierrec/lz4/v4` |
| `.br` | ✓ | ✓ | `andybalholm/brotli` |
| `.lz` / `.lzma` | ✓ | ✓ | lzip 自拼头 + `ulikunitz/xz/lzma` |

说明：

- **rar 只读**：7-Zip 本身也不创建 rar（专有格式 + unRAR 许可限制），本工具同样不创建。
- **7z 可读写，但写入是有取舍的**。写入库只实现 Store 与 LZMA2 两种方法，且头部子集有限，因此：
  - `-p` 加密**不支持**（无 AES-256 / 加密头），需要加密请用 `.zip -p`；
  - **不支持 solid 块**（每个条目独立流）、**不记录修改时间**、**不保留符号链接类型**（退化为内容是目标路径的普通文件）；
  - `-so` 直写 stdout 不支持（7z 头部要回填开头 32 字节，需可寻址输出）；
  - 不支持 BCJ / Delta 过滤器与 PPMd / BZip2 方法。
  以上限制都会在触发时给出明确提示，不会静默降级。
- **7z 写入的选项映射**：`-level 0` = Store（7-Zip 叫 Copy），`1-9` = LZMA2 并按级别调字典（1→64K，5→16M，9→256M），可用 `-dict` 或 `-method` 显式覆盖；`-mx9` 是 `-level 9` 的别名。
- 生成的 7z 已用 `7z.exe t` / `7z.exe l -slt` / `7z.exe x` 交叉验证，含空文件、空目录、中文名、300 KB 随机数据与分卷。
- `-t` 完整性测试：zip 校验每个条目的 CRC / AES 认证码；tar 与单文件流校验解压全程；rar/7z 校验包内 CRC；不落盘。
- 格式识别：按后缀 → 魔数嗅探核对；`-type` 可强制指定，后缀与内容不符时按实际内容处理并提示。
- 依赖版本固定在 `go.mod`。
- **紧贴写法**：`-oDIR` / `-pPASS` / `-v100m` / `-mx9` 与 7-Zip 一致（含 `=` 的 `-o=DIR` 同样可用）。

## 默认输出规则与安全

- 解压到当前目录（`-o` 可改）；默认跳过已存在文件（`-ao` 精细控制）。
- 压缩：输入在当前目录内保留相对路径，目录外只保留基名。
- **Zip-Slip 防护**：拦截绝对路径、盘符路径、`../` 跳出输出目录的条目。
- **symlink 安全**：拒绝目标跳出输出根目录（参考 CVE-2025-55188）；Windows 无 mklink 权限时警告跳过。
- 失败不留半截文件；包内修改（d/u/rn）先写临时文件再原子替换，失败自动回滚。

## 项目结构

```text
xunzip/
  go.mod / go.sum       独立 module 与依赖校验
  main.go               CLI 分发、退出码（0/1/2/7）、7z 风格命令
  cli.go                flag 解析、命令检测、@listfile
  options.go            全局选项结构与覆盖模式
  format.go             格式识别、i 命令
  compression.go        压缩器工厂（gzip/xz/zstd/bzip2/zip 级别）
  extraformats.go       lz4/brotli/lzip/lzma 包装
  extract.go            zip/tar/单文件流解压、列出、完整性测试、Zip-Slip
  extract_extra.go      rar/7z 解压与列出
  compress.go           zip/tar/单文件流压缩
  sevenzip_write.go     7z 写入（Store/LZMA2，go-filesystems/sevenzip）
  overwrite.go          -ao 四态覆盖
  modify.go             a/d/u/rn 包内增删改
  volumes.go            -v 分卷切分与合并
  pipes.go              -si/-so 管道
  filter.go             -i/-x 包含/排除
  listdetail.go         -slt / --json 详细列表
  hash.go               h 哈希命令
  bench.go              b 基准测试
  motw.go               Mark-of-the-Web
  console_windows.go    控制台 UTF-8 切换
  .github/workflows/    CI（格式/vet/冒烟/交叉编译）与 Release（多架构打包发布）
  README.md             本文件
```

## 发布流程

推 `v*` 标签即自动交叉编译 12 个目标并创建 GitHub Release：

```bash
git tag v1.3.0
git push origin v1.3.0
```

版本号通过 `-ldflags "-X main.version=..."` 注入（`main.version` 因此是 `var` 而非 `const`），
Release 任务会在 `linux/amd64` 产物上实际执行 `-version` 自检，注入失败即中断发布。
普通推送与 PR 走 CI：`gofmt` / `go vet` / 全格式往返冒烟 / 全矩阵交叉编译。
