package archive

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsupportedFormat 当文件不是支持的压缩包格式时返回。
var ErrUnsupportedFormat = errors.New("unsupported archive format")

// Kind 标识压缩包类型。
type Kind int

const (
	KindUnknown Kind = iota
	KindZip
	KindTar
	KindTarGz
	KindGzip
)

// ArchiveExtensions 是批量扫描时识别的扩展名集合（按长后缀优先）。
var ArchiveExtensions = []string{".tar.gz", ".tgz", ".tar", ".zip", ".gz"}

// Detect 依据扩展名判定压缩包类型。
func Detect(path string) Kind {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(name, ".zip"):
		return KindZip
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return KindTarGz
	case strings.HasSuffix(name, ".tar"):
		return KindTar
	case strings.HasSuffix(name, ".gz"):
		return KindGzip
	}
	return KindUnknown
}

// IsSupportedArchive 判断给定路径是否为受支持的压缩包。
func IsSupportedArchive(path string) bool {
	return Detect(path) != KindUnknown
}

// Extract 解压 src 到 targetDir。智能合并：若压缩包仅含单一顶层目录，
// 其内容直接并入 targetDir（避免双层嵌套）。passwords 用于加密 zip 的轮询尝试。
func Extract(ctx context.Context, src, targetDir string, passwords []string, p ProgressFn) error {
	switch Detect(src) {
	case KindZip:
		return extractZip(ctx, src, targetDir, passwords, p)
	case KindTar:
		return extractTar(ctx, src, targetDir, p, false)
	case KindTarGz:
		return extractTar(ctx, src, targetDir, p, true)
	case KindGzip:
		return extractGzipSingle(ctx, src, targetDir, p)
	}
	return ErrUnsupportedFormat
}

// entryMeta 描述压缩包内单个条目的元信息（扫描阶段产出）。
type entryMeta struct {
	name  string
	isDir bool
	size  int64
}

// normName 规范化条目路径：转正斜杠并去尾部斜杠。
func normName(name string) string {
	n := filepath.ToSlash(name)
	n = strings.TrimSuffix(n, "/")
	return n
}

// determineStrip 返回应剥离的顶层目录名（无则空串）。
// 规则：当且仅当所有条目共享唯一的顶层段 X，且至少一个条目位于 X/ 之下，
// 且不存在名为 X 的根文件时，剥离 X（智能合并单顶层目录）。
// 这同时覆盖「显式目录条目」与「仅含 root/a.txt 这类隐式目录」两种情况。
func determineStrip(entries []entryMeta) string {
	if len(entries) == 0 {
		return ""
	}
	tops := map[string]struct{}{}
	anyUnder := false // 是否存在带 "/" 的条目（即真正位于某前缀下）
	for _, e := range entries {
		n := normName(e.name)
		if n == "" {
			continue
		}
		top := n
		if idx := strings.IndexByte(n, '/'); idx >= 0 {
			top = n[:idx]
			anyUnder = true
		}
		tops[top] = struct{}{}
	}
	if len(tops) != 1 || !anyUnder {
		return ""
	}
	var x string
	for k := range tops {
		x = k
	}
	// 若存在名为 X 的根文件（非目录），则 X 不是目录容器，不剥离
	for _, e := range entries {
		if normName(e.name) == x && !e.isDir {
			return ""
		}
	}
	return x
}

// resolveEntry 将条目名映射到 targetDir 内的绝对目标路径，处理剥离与安全校验。
// 返回 (dest, skip, err)：skip=true 表示该条目无需写出（如被剥离的顶层目录本身）。
func resolveEntry(targetDir, name, strip string) (dest string, skip bool, err error) {
	n := normName(name)
	if strip != "" {
		if n == strip {
			return "", true, nil // 顶层目录条目本身被剥离，跳过
		}
		if strings.HasPrefix(n, strip+"/") {
			n = strings.TrimPrefix(n, strip+"/")
		}
	}
	if n == "" {
		return "", true, nil
	}
	clean := filepath.Clean(filepath.FromSlash(n))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", false, fmt.Errorf("unsafe entry path: %q", name)
	}
	dest = filepath.Join(targetDir, clean)
	rel, err := filepath.Rel(targetDir, dest)
	if err != nil {
		return "", false, err
	}
	if strings.HasPrefix(filepath.ToSlash(rel), "..") {
		return "", false, fmt.Errorf("unsafe entry path: %q", name)
	}
	return dest, false, nil
}
