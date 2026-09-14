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

// IncompressibleExtensions 是「已压缩 / 本身不可再压缩」的常见扩展名集合（小写、含点）。
// 压缩时若开启「跳过已压缩文件」选项，这些文件会被直接排除——二次压缩既浪费 CPU、
// 又常因 Deflate 无法再压缩而体积反增。覆盖压缩包、已压缩的图片/音视频、zip 系文档等。
// 注意：这是启发式列表，目的是跳过明显已压缩的格式；未列出的格式仍会正常参与压缩。
var IncompressibleExtensions = map[string]bool{
	// 压缩 / 归档
	".zip": true, ".zipx": true, ".7z": true, ".rar": true, ".tar": true, ".gz": true,
	".tgz": true, ".tar.gz": true, ".bz2": true, ".tbz2": true, ".xz": true, ".txz": true,
	".lz4": true, ".zst": true, ".zstd": true, ".lzma": true, ".lzh": true, ".lha": true,
	".cab": true, ".iso": true, ".jar": true, ".war": true, ".apk": true, ".deb": true,
	".rpm": true, ".ace": true, ".arj": true, ".z": true, ".dz": true, ".lz": true,
	// 图片（已压缩）
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".heic": true,
	".heif": true, ".avif": true, ".jp2": true, ".jpx": true, ".jxr": true, ".wdp": true,
	// 音视频（已压缩）
	".mp3": true, ".mp4": true, ".m4a": true, ".m4v": true, ".mov": true, ".avi": true,
	".mkv": true, ".wmv": true, ".wma": true, ".flv": true, ".webm": true, ".m2ts": true,
	".ts": true, ".vob": true, ".3gp": true, ".mpg": true, ".mpeg": true, ".ogv": true,
	".oga": true, ".ogg": true, ".opus": true, ".ac3": true, ".dts": true, ".aac": true,
	".flac": true, ".ape": true,
	// 文档（zip 系 / 已压缩）
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true, ".docm": true, ".xlsm": true,
	".pptm": true, ".odt": true, ".ods": true, ".odp": true, ".odg": true, ".odf": true,
	".epub": true, ".mobi": true, ".azw": true, ".azw3": true, ".djvu": true, ".cbz": true,
	".cbr": true, ".fb2": true,
}

// IsIncompressible 判断文件名是否为「已压缩 / 本身不可再压缩」的类型，
// 用于压缩时「跳过已压缩文件」选项。扩展名大小写不敏感；无扩展名视为可压缩。
func IsIncompressible(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return IncompressibleExtensions[ext]
}

// Limits 约束单次解压的资源消耗（零值 = 不限制）。
type Limits struct {
	// MaxTotalBytes 单任务解压总字节上限（zip bomb 防护），0 表示不限制。
	MaxTotalBytes int64
	// MaxRatio 允许的最大压缩率（解压后大小 / 原压缩包大小），0 表示不限制。
	// 用于拦截解压炸弹：很小的压缩包展开成超大文件时比率极高。
	// 注意它只看比率、不限绝对大小，因此合法的大体积压缩包（如 300G 归档）
	// 只要比率正常仍可通过。
	MaxRatio int64
}

// Extract 解压 src 到 targetDir。智能合并：若压缩包仅含单一顶层目录，
// 其内容直接并入 targetDir（避免双层嵌套）。passwords 用于加密 zip 的轮询尝试。
func Extract(ctx context.Context, src, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	switch Detect(src) {
	case KindZip:
		return extractZip(ctx, src, targetDir, passwords, limits, p)
	case KindTar:
		return extractTar(ctx, src, targetDir, limits, p, false)
	case KindTarGz:
		return extractTar(ctx, src, targetDir, limits, p, true)
	case KindGzip:
		return extractGzipSingle(ctx, src, targetDir, limits, p)
	}
	return ErrUnsupportedFormat
}

// StripArchiveExt 去除压缩包扩展名（长后缀优先、大小写不敏感，保留原名大小写）。
func StripArchiveExt(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range ArchiveExtensions {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
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
