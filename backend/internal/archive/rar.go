package archive

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

// rarSource 抽象 rar 归档的遍历与读取：*rardecode.Reader 与 *rardecode.ReadCloser 均满足，
// 使"从文件路径打开"与"从 ReaderAt（分卷/嗅探）打开"共用同一套解压逻辑。
type rarSource interface {
	Next() (*rardecode.FileHeader, error)
	Read(p []byte) (int, error)
}

// rarOpener 以给定密码（空串表示无密码）打开归档，返回归档对象与关闭函数。
type rarOpener func(password string) (rarSource, func(), error)

// fileRarOpener 从文件系统路径打开 rar。
func fileRarOpener(src string) rarOpener {
	return func(password string) (rarSource, func(), error) {
		var rc *rardecode.ReadCloser
		var err error
		if password == "" {
			rc, err = rardecode.OpenReader(src)
		} else {
			rc, err = rardecode.OpenReader(src, rardecode.Password(password))
		}
		if err != nil {
			return nil, nil, err
		}
		return rc, func() { rc.Close() }, nil
	}
}

// extractRar 解压单个 RAR 归档（含加密、头部加密）到 targetDir。
func extractRar(ctx context.Context, src, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	return extractRarVolumes(ctx, []string{src}, targetDir, passwords, limits, p)
}

// extractRarVolumes 解压 RAR 分卷集：vols[0] 必须是首卷，其余卷由 rardecode 按命名自行续接。
// 注意：RAR 每卷都自带归档头，不能像 zip 分卷那样拼接成单一字节流，故此处只传首卷。
// rardecode 的条目是流式推进的（无法随机重复遍历），故分两遍：
// 第一遍仅扫描条目以计算智能合并前缀与总量（进度/限额），第二遍才真正写出文件。
func extractRarVolumes(ctx context.Context, vols []string, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	if len(vols) == 0 {
		return fmt.Errorf("open rar: 分卷列表为空")
	}
	var total int64
	for _, v := range vols {
		if fi, err := os.Stat(v); err == nil {
			total += fi.Size()
		}
	}
	return extractRarWith(ctx, fileRarOpener(vols[0]), total, targetDir, passwords, limits, p)
}

func extractRarWith(ctx context.Context, open rarOpener, total int64, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}

	// 先确定密码：头部加密（-hp）时无密码打开即失败，需按密码列表逐个探测。
	password, err := resolveRarPassword(open, passwords)
	if err != nil {
		return err
	}

	entries, err := scanRarEntries(open, password)
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
	if limits.MaxRatio > 0 && total > 0 {
		ratio := float64(totalBytes) / float64(total)
		if ratio > float64(limits.MaxRatio) {
			return fmt.Errorf("%w：压缩率 %.0f 超过上限 %d（解压后约 %d 字节 / 压缩包 %d 字节）",
				ErrLimitExceeded, ratio, limits.MaxRatio, totalBytes, total)
		}
	}
	t := newTracker(totalBytes, p)

	rr, closeFn, err := open(password)
	if err != nil {
		return fmt.Errorf("open rar: %w", err)
	}
	defer closeFn()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read rar entry: %w", err)
		}
		name := normalizeSep(h.Name)
		isDir := h.IsDir || strings.HasSuffix(name, "/")
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
		out, cerr := os.Create(dest)
		if cerr != nil {
			return cerr
		}
		cw := &countWriter{w: out, t: t}
		maxBytes := int64(-1)
		if limits.MaxTotalBytes > 0 {
			maxBytes = limits.MaxTotalBytes - t.processedBytes
		}
		_, copyErr := copyWithLimit(ctx, cw, rr, maxBytes)
		closeErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("write entry %q: %w", name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %q: %w", dest, closeErr)
		}
	}
	t.notify(true)
	return nil
}

// resolveRarPassword 确定打开归档所需的密码，无需密码时返回空串。
// 判定顺序：先无密码打开并看首个条目是否加密；否则按密码列表逐个探测，
// 探测以"能否解出第一个文件内容"为准（头部能打开但密码不对会在读取时才暴露）。
func resolveRarPassword(open rarOpener, passwords []string) (string, error) {
	var lastErr error
	if rr, closeFn, err := open(""); err == nil {
		need := rarNeedsPassword(rr)
		closeFn()
		if !need {
			return "", nil
		}
	} else {
		lastErr = err
	}

	if len(passwords) == 0 {
		return "", fmt.Errorf("%w（rar：%v）", ErrPasswordRequired, lastErr)
	}
	for _, pw := range passwords {
		if e := rarProbe(open, pw); e == nil {
			return pw, nil
		} else {
			lastErr = e
		}
	}
	return "", fmt.Errorf("%w（rar：%v）", ErrPasswordRequired, lastErr)
}

// rarNeedsPassword 判断已打开的归档是否需要密码（首个条目加密或头部加密）。
func rarNeedsPassword(rr rarSource) bool {
	h, err := rr.Next()
	if err != nil {
		// 头部加密时无密码打开会在 Next 阶段即报错
		return true
	}
	return h.Encrypted || h.HeaderEncrypted
}

// rarProbe 用指定密码试解归档的第一个文件内容，成功返回 nil。
// 密码错误在读取时才会暴露（rardecode.ErrBadPassword），故必须真实读一个字节。
func rarProbe(open rarOpener, password string) error {
	rr, closeFn, err := open(password)
	if err != nil {
		return err
	}
	defer closeFn()
	for {
		h, err := rr.Next()
		if err == io.EOF {
			return nil // 空归档：无内容可校验，视为通过
		}
		if err != nil {
			return err
		}
		if h.IsDir || h.UnPackedSize == 0 {
			continue
		}
		_, err = io.CopyN(io.Discard, rr, 1)
		if err != nil && err != io.EOF {
			return err
		}
		return nil
	}
}

// scanRarEntries 只读地枚举归档全部条目（不写出任何文件）。
func scanRarEntries(open rarOpener, password string) ([]entryMeta, error) {
	rr, closeFn, err := open(password)
	if err != nil {
		return nil, fmt.Errorf("open rar: %w", err)
	}
	defer closeFn()
	var entries []entryMeta
	for {
		h, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read rar entry: %w", err)
		}
		entries = append(entries, entryMeta{
			name:  normalizeSep(h.Name),
			isDir: h.IsDir || strings.HasSuffix(h.Name, "/"),
			size:  h.UnPackedSize,
		})
	}
	return entries, nil
}

// fileSize 返回文件大小，用于压缩率限额计算。
func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
