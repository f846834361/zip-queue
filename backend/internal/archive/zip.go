package archive

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	zip "github.com/alexmullins/zip"
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

	// 探测是否有加密条目，若有则按顺序轮询密码
	password := ""
	for _, f := range r.File {
		if !f.IsEncrypted() {
			continue
		}
		// 找到第一个加密条目，用其探测正确密码
		pw, err := tryPasswords(r, passwords)
		if err != nil {
			return err
		}
		password = pw
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
			f.SetPassword(password)
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

// tryPasswords 用每个密码尝试打开加密条目，返回第一个成功打开的密码。
func tryPasswords(r *zip.ReadCloser, passwords []string) (string, error) {
	if len(passwords) == 0 {
		return "", ErrPasswordRequired
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
		// 只有加密目录条目，无文件可探测，直接用第一个密码
		return passwords[0], nil
	}
	for _, pw := range passwords {
		probe.SetPassword(pw)
		rc, err := probe.Open()
		if err == nil {
			rc.Close()
			return pw, nil
		}
	}
	return "", ErrPasswordRequired
}

// 压缩实现见 compress.go：写入改用标准库 archive/zip，以便按压缩效率档位
// 指定 Deflate 级别或仅打包（Store）；本文件只负责加密 zip 的解压。
