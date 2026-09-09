package archive

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// scanTar 枚举 tar(tar.gz) 头部信息（不写盘），用于确定剥离前缀与总量。
func scanTar(src string, gz bool) ([]entryMeta, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rd io.Reader = f
	if gz {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gzr.Close()
		rd = gzr
	}
	tr := tar.NewReader(rd)
	var entries []entryMeta
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		isDir := h.Typeflag == tar.TypeDir
		entries = append(entries, entryMeta{name: h.Name, isDir: isDir, size: h.Size})
		if _, err := io.CopyN(io.Discard, tr, h.Size); err != nil && err != io.EOF {
			return nil, err
		}
	}
	return entries, nil
}

// extractTar 解压 tar（gz=true 时为 tar.gz）到 targetDir，应用智能合并。
func extractTar(ctx context.Context, src, targetDir string, limits Limits, p ProgressFn, gz bool) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	entries, err := scanTar(src, gz)
	if err != nil {
		return err
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

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	var rd io.Reader = f
	if gz {
		gzr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gzr.Close()
		rd = gzr
	}
	tr := tar.NewReader(rd)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		dest, skip, err := resolveEntry(targetDir, h.Name, strip)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			out, err := os.Create(dest)
			if err != nil {
				return err
			}
			cw := &countWriter{w: out, t: t}
			// 单条目可写字节数 = 剩余额度（未配置上限时 -1 表示不限）
			maxBytes := int64(-1)
			if limits.MaxTotalBytes > 0 {
				maxBytes = limits.MaxTotalBytes - t.processedBytes
			}
			_, copyErr := copyWithLimit(ctx, cw, tr, maxBytes)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return fmt.Errorf("close %q: %w", dest, closeErr)
			}
			_ = os.Chmod(dest, os.FileMode(h.Mode).Perm())
		default:
			// 符号链接/硬链接/其他类型跳过（安全考虑）
		}
	}
	t.notify(true)
	return nil
}
