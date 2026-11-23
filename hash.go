package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// hashCmd 计算文件或压缩包的哈希，对齐 7-Zip 的 h 命令。
func hashOf(path string, algo string) (string, int64, error) {
	var h hash.Hash
	switch strings.ToUpper(strings.TrimSpace(algo)) {
	case "CRC32":
		h = crc32.NewIEEE()
	case "CRC64":
		h = crc64.New(crc64.MakeTable(crc64.ISO))
	case "SHA1":
		h = sha1.New()
	case "SHA256", "":
		h = sha256.New()
	case "SHA512":
		h = sha512.New()
	case "MD5":
		h = md5.New()
	default:
		return "", 0, fmt.Errorf("不支持的哈希算法 %q（支持 CRC32/CRC64/SHA1/SHA256/SHA512/MD5）", algo)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), n, nil
}

func runHash(p parsedCLI) int {
	if len(p.args) == 0 {
		fmt.Fprintln(os.Stderr, "错误: h 命令需要至少一个文件")
		return 7
	}
	algo := p.opt.hash
	if algo == "" {
		algo = "SHA256"
	}
	var errs []error
	for _, path := range p.args {
		fi, err := os.Stat(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if fi.IsDir() {
			// 目录：遍历所有普通文件。
			err = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				sum, _, err := hashOf(p, algo)
				if err != nil {
					return err
				}
				fmt.Printf("%s  %s\n", sum, p)
				return nil
			})
			if err != nil {
				errs = append(errs, err)
			}
			continue
		}
		sum, _, err := hashOf(path, algo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		fmt.Printf("%s  %s\n", sum, path)
	}
	return aggregateResult(false, errs)
}
