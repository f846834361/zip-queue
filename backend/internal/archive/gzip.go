package archive

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractGzipSingle 解压单文件 .gz（非 .tar.gz）到 targetDir，
// 产物文件名为去掉 .gz 后缀的原 basename。进度不确定（totalBytes=0）。
func extractGzipSingle(ctx context.Context, src, targetDir string, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	base := strings.TrimSuffix(filepath.Base(src), ".gz")
	if base == "" {
		base = "decompressed"
	}
	dest := filepath.Join(targetDir, base)

	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gzr.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	t := newTracker(0, 1, p) // totalBytes=0 -> 不确定进度
	t.setCurrent(base)
	cw := &countWriter{w: out, t: t}
	if _, err := io.Copy(cw, gzr); err != nil {
		return err
	}
	_ = os.Chmod(dest, 0o644)
	t.entryDone()
	t.notify(true)
	return nil
}
