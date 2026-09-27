package archive

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	zip7 "github.com/bodgit/sevenzip"
)

// extract7z 解压 7z 归档（单文件或分卷拼接后的 ra）到 targetDir。
// 条目名中的反斜杠统一为正斜杠，以便复用 resolveEntry 的安全校验与智能合并逻辑。
func extract7z(ctx context.Context, ra io.ReaderAt, total int64, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	zr, err := open7zReader(ra, total, passwords)
	if err != nil {
		return fmt.Errorf("open 7z: %w", err)
	}

	// 扫描条目，确定剥离前缀与总量
	var entries []entryMeta
	for _, f := range zr.File {
		name := normalizeSep(f.Name)
		isDir := f.Mode().IsDir() || strings.HasSuffix(name, "/")
		entries = append(entries, entryMeta{name: name, isDir: isDir, size: int64(f.UncompressedSize)})
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
	if limits.MaxRatio > 0 && total > 0 {
		ratio := float64(totalBytes) / float64(total)
		if ratio > float64(limits.MaxRatio) {
			return fmt.Errorf("%w：压缩率 %.0f 超过上限 %d（解压后约 %d 字节 / 压缩包 %d 字节）", ErrLimitExceeded, ratio, limits.MaxRatio, totalBytes, total)
		}
	}
	t := newTracker(totalBytes, p)

	for _, f := range zr.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := normalizeSep(f.Name)
		isDir := f.Mode().IsDir() || strings.HasSuffix(name, "/")
		dest, skip, rerr := resolveEntry(targetDir, name, strip)
		if rerr != nil {
			return rerr
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
		rc, oerr := f.Open()
		if oerr != nil {
			return fmt.Errorf("open entry %q: %w", name, oerr)
		}
		out, cerr := os.Create(dest)
		if cerr != nil {
			rc.Close()
			return cerr
		}
		cw := &countWriter{w: out, t: t}
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
	}
	t.notify(true)
	return nil
}

// open7zReader 打开 7z 归档；无密码打开失败（可能加密）时按密码列表逐个尝试。
func open7zReader(ra io.ReaderAt, total int64, passwords []string) (*zip7.Reader, error) {
	zr, err := zip7.NewReader(ra, total)
	if err == nil {
		return zr, nil
	}
	var lastErr = err
	for _, pw := range passwords {
		if zr, e := zip7.NewReaderWithPassword(ra, total, pw); e == nil {
			return zr, nil
		} else {
			lastErr = e
		}
	}
	return nil, lastErr
}

// normalizeSep 将 7z 条目名中的反斜杠统一为正斜杠，复用 ZIP 路径处理与安全校验。
func normalizeSep(name string) string {
	return strings.ReplaceAll(name, "\\", "/")
}
