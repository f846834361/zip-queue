package archive

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	zip "github.com/alexmullins/zip"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// ErrPasswordRequired 当 zip 加密但无匹配密码时返回。
var ErrPasswordRequired = fmt.Errorf("zip is encrypted but no matching password found")

// extractZip 解压 zip 到 targetDir，应用智能合并；支持加密 zip（按密码列表顺序尝试）。
func extractZip(ctx context.Context, src, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	// 探测是否有加密条目，若有则按顺序轮询密码（UTF-8 失败回退 GBK）
	password := ""
	passwordGBK := false
	for _, f := range r.File {
		if !f.IsEncrypted() {
			continue
		}
		// 找到第一个加密条目，用其探测正确密码
		pw, gbk, err := tryPasswords(r, passwords)
		if err != nil {
			return err
		}
		password = pw
		passwordGBK = gbk
		break
	}

	// 扫描条目以确定剥离前缀与总量
	var entries []entryMeta
	for _, f := range r.File {
		entries = append(entries, entryMeta{
			name:  f.Name,
			isDir: f.Mode().IsDir() || strings.HasSuffix(f.Name, "/"),
			size:  int64(f.UncompressedSize64),
		})
	}
	strip := determineStrip(entries)
	var totalBytes int64
	for _, e := range entries {
		if !e.isDir {
			totalBytes += e.size
		}
	}
	if limits.MaxTotalBytes > 0 && totalBytes > limits.MaxTotalBytes {
		return fmt.Errorf("%w：压缩包解压后约 %d 字节，超过上限 %d", ErrLimitExceeded, totalBytes, limits.MaxTotalBytes)
	}
	// 压缩率限制：解压后估算大小 / 原压缩包大小 超过阈值即视为解压炸弹
	if limits.MaxRatio > 0 {
		if fi, serr := os.Stat(src); serr == nil && fi.Size() > 0 {
			ratio := float64(totalBytes) / float64(fi.Size())
			if ratio > float64(limits.MaxRatio) {
				return fmt.Errorf("%w：压缩率 %.0f 超过上限 %d（解压后约 %d 字节 / 压缩包 %d 字节）",
					ErrLimitExceeded, ratio, limits.MaxRatio, totalBytes, fi.Size())
			}
		}
	}
	t := newTracker(totalBytes, p)

	for _, f := range r.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		isDir := f.Mode().IsDir() || strings.HasSuffix(f.Name, "/")
		dest, skip, err := resolveEntry(targetDir, f.Name, strip)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		if isDir {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		var rc io.ReadCloser
		if f.IsEncrypted() {
			setEntryPassword(f, password, passwordGBK)
		}
		rc, err = f.Open()
		if err != nil {
			return fmt.Errorf("open entry %q: %w", f.Name, err)
		}
		out, err := os.Create(dest)
		if err != nil {
			rc.Close()
			return err
		}
		cw := &countWriter{w: out, t: t}
		// 单条目可写字节数 = 剩余额度（未配置上限时 -1 表示不限）
		maxBytes := int64(-1)
		if limits.MaxTotalBytes > 0 {
			maxBytes = limits.MaxTotalBytes - t.processedBytes
		}
		_, copyErr := copyWithLimit(ctx, cw, rc, maxBytes)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return fmt.Errorf("close %q: %w", dest, closeErr)
		}
		_ = os.Chmod(dest, f.Mode().Perm())
	}
	t.notify(true)
	return nil
}

// setEntryPassword 为加密条目设置密码。gbk=true 时按 GBK 编码成字节，
// 以兼容少数把中文密码按 GBK 编码的非规范 AES 包（标准 AES 密码应为 UTF-8）。
// 注：Go 的 []byte(string) 与 string([]byte) 为逐字节保留，故把 GBK 字节包成 string 后，
// 库内再转回 []byte 仍得到原始 GBK 字节。
func setEntryPassword(f *zip.File, pw string, gbk bool) {
	if gbk {
		if b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(pw)); err == nil {
			f.SetPassword(string(b))
			return
		}
	}
	f.SetPassword(pw)
}

// tryPasswords 用每个密码尝试打开加密条目，返回第一个成功打开的密码及其编码。
// 第一遍全部密码按 UTF-8 尝试；若全部失败，第二遍按 GBK 编码回退（兼容中文密码）。
// gbk=false 表示 UTF-8 命中，true 表示 GBK 命中。
func tryPasswords(r *zip.ReadCloser, passwords []string) (string, bool, error) {
	if len(passwords) == 0 {
		return "", false, ErrPasswordRequired
	}
	// 找到第一个加密的非目录条目作为探针
	var probe *zip.File
	for _, f := range r.File {
		if f.IsEncrypted() && !f.Mode().IsDir() && !strings.HasSuffix(f.Name, "/") {
			probe = f
			break
		}
	}
	if probe == nil {
		// 只有加密目录条目，无文件可探测，直接用第一个密码（UTF-8）
		return passwords[0], false, nil
	}
	// 第一遍：UTF-8
	for _, pw := range passwords {
		probe.SetPassword(pw)
		if rc, err := probe.Open(); err == nil {
			rc.Close()
			return pw, false, nil
		}
	}
	// 第二遍：全部 UTF-8 失败后，按 GBK 回退
	for _, pw := range passwords {
		b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(pw))
		if err != nil {
			continue
		}
		probe.SetPassword(string(b))
		if rc, err := probe.Open(); err == nil {
			rc.Close()
			return pw, true, nil
		}
	}
	return "", false, ErrPasswordRequired
}

// 压缩实现见 compress.go：写入改用标准库 archive/zip，以便按压缩效率档位
// 指定 Deflate 级别或仅打包（Store）；本文件只负责加密 zip 的解压。
