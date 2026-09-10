package archive

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// extractGzipSingle 解压单文件 .gz（非 .tar.gz）到 targetDir，
// 产物文件名为去掉 .gz 后缀的原 basename（大小写不敏感）。
// 进度不确定（totalBytes=0）；gzip 流无法预知解压后大小，
// 资源上限由拷贝过程中的累计字节数约束。
func extractGzipSingle(ctx context.Context, src, targetDir string, limits Limits, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	base := StripArchiveExt(filepath.Base(src))
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
	// 兜底关闭；成功路径下方显式 Close 并检查错误
	defer out.Close()

	t := newTracker(0, p) // totalBytes=0 -> 不确定进度
	cw := &countWriter{w: out, t: t}
	// 字节上限：绝对上限与按压缩率推导的上限取较小者；均未配置则为 -1（不限）。
	// 注意不能把 0 当作“不限”传给 copyWithLimit（其 maxBytes>=0 即视为有限额 0）。
	maxBytes := int64(-1)
	if limits.MaxTotalBytes > 0 {
		maxBytes = limits.MaxTotalBytes
	}
	if limits.MaxRatio > 0 {
		if fi, serr := os.Stat(src); serr == nil && fi.Size() > 0 {
			if ratioCap := int64(float64(fi.Size()) * float64(limits.MaxRatio)); maxBytes < 0 || ratioCap < maxBytes {
				maxBytes = ratioCap
			}
		}
	}
	if _, err := copyWithLimit(ctx, cw, gzr, maxBytes); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %q: %w", dest, err)
	}
	_ = os.Chmod(dest, 0o644)
	t.notify(true)
	return nil
}
