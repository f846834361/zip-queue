package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	KindSevenZip
	KindRar
)

// String 返回类型的可读名称，用于报错与日志。
func (k Kind) String() string {
	switch k {
	case KindZip:
		return "zip"
	case KindTar:
		return "tar"
	case KindTarGz:
		return "tar.gz"
	case KindGzip:
		return "gzip"
	case KindSevenZip:
		return "7z"
	case KindRar:
		return "rar"
	}
	return "unknown"
}

// ArchiveExtensions 是批量扫描时识别的扩展名集合（按长后缀优先）。
var ArchiveExtensions = []string{".tar.gz", ".tgz", ".tar", ".zip", ".7z", ".rar", ".gz"}

// splitVolumeInfo 识别分卷文件名，返回逻辑基名 base、卷序号 idx、类型 kind 与 ok。
// 不依赖具体归档格式（格式由解压时的魔数嗅探决定），支持：
//   - 容器扩展名分卷：base.<ext>.NNN（ext ∈ 已知容器扩展名，如 zip/7z/rar/tar），
//     例：name.zip.001、name.7z.001、name.rar.001；
//   - PKWARE 分卷：base.zNN（末卷为 base.zip）；
//   - 通用数字分卷：base.NNN（无容器扩展名，如 split 切分出的 name.001）。
//
// 数字段长度需 ≥ 2 以过滤 file.1 这类非分卷。非分卷返回 ok=false。
func splitVolumeInfo(name string) (base string, idx int, kind string, ok bool) {
	lower := strings.ToLower(name)
	// 容器扩展名 + 数字分卷：base.<ext>.NNN
	if dot := strings.LastIndex(lower, "."); dot > 0 && dot < len(lower)-1 {
		suf := lower[dot+1:]
		if isAllDigits(suf) && len(suf) >= 2 {
			pre := lower[:dot] // base.<ext>
			if d2 := strings.LastIndex(pre, "."); d2 >= 0 {
				if isKnownContainerExt(pre[d2+1:]) {
					n, _ := strconv.Atoi(suf)
					return name[:d2] + "." + name[d2+1:dot], n, "split", true
				}
			}
			// 通用数字分卷：base.NNN（无容器扩展名）
			n, _ := strconv.Atoi(suf)
			return name[:dot], n, "split", true
		}
	}
	// PKWARE 分卷：base.zNN
	if i := strings.LastIndex(lower, ".z"); i >= 0 {
		num := lower[i+2:]
		if num != "" && isAllDigits(num) && len(num) >= 2 {
			n, _ := strconv.Atoi(num)
			return name[:i], n, "pkware", true
		}
	}
	// RAR 新式分卷：base.partN.rar / base.partNN.rar / base.partNofM.rar
	if i := strings.LastIndex(lower, ".part"); i >= 0 {
		rest := lower[i+len(".part"):]
		if dot := strings.IndexByte(rest, '.'); dot > 0 {
			num := rest[:dot]
			ext := strings.TrimSuffix(rest[dot+1:], ".rar")
			// partN.rar：ext 为空（rest 形如 "1.rar"）；partNofM.rar：ext 形如 "of5"
			if isAllDigits(num) && (ext == "rar" || strings.HasPrefix(ext, "of")) {
				n, _ := strconv.Atoi(num)
				return name[:i], n, "rarmulti", true
			}
		}
	}
	// RAR 旧式分卷续卷：base.rNN（首卷为 base.rar，续卷从 .r00 起）
	if i := strings.LastIndex(lower, ".r"); i >= 0 {
		num := lower[i+2:]
		if len(num) >= 2 && isAllDigits(num) {
			n, _ := strconv.Atoi(num)
			return name[:i], n + 1, "rarold", true // .r00 记为序 1，与首卷 .rar（序 0）衔接
		}
	}
	return "", 0, "", false
}

// isKnownContainerExt 判断扩展名是否为已知归档容器（用于容器扩展名分卷识别）。
func isKnownContainerExt(ext string) bool {
	switch ext {
	case "zip", "7z", "rar", "tar", "tgz", "gz", "bz2", "xz", "lz4", "zst", "tar.gz":
		return true
	}
	return false
}

// isAllDigits 判断字符串是否全为数字且非空。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Detect 依据扩展名判定单文件压缩包类型（分卷不在此判定，交由 IsSupportedArchive）。
func Detect(path string) Kind {
	name := filepath.Base(path)
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".7z"):
		return KindSevenZip
	case strings.HasSuffix(lower, ".rar"):
		return KindRar
	case strings.HasSuffix(lower, ".zip"):
		return KindZip
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return KindTarGz
	case strings.HasSuffix(lower, ".tar"):
		return KindTar
	case strings.HasSuffix(lower, ".gz"):
		return KindGzip
	}
	return KindUnknown
}

// DetectMagic 读取文件头魔数判定真实格式，用于纠正被误标的扩展名。
// 仅识别魔数唯一、无歧义的格式（zip / 7z / rar）；无法确定时返回 KindUnknown。
func DetectMagic(path string) Kind {
	f, err := os.Open(path)
	if err != nil {
		return KindUnknown
	}
	defer f.Close()
	buf := make([]byte, 8)
	n, _ := io.ReadFull(f, buf)
	if n < 4 {
		return KindUnknown // 空文件或不足一个魔数长度：交给扩展名/解码器报更准确的错
	}
	switch {
	case isZipMagic(buf):
		return KindZip
	case is7zMagic(buf):
		return KindSevenZip
	case isRarMagic(buf):
		return KindRar
	}
	return KindUnknown
}

// isRarMagic 判断是否为 RAR 签名（Rar!\x1A\x07，RAR4 与 RAR5 共用前 6 字节）。
func isRarMagic(b []byte) bool {
	return len(b) >= 6 && b[0] == 'R' && b[1] == 'a' && b[2] == 'r' &&
		b[3] == '!' && b[4] == 0x1A && b[5] == 0x07
}

// IsSupportedArchive 判断给定路径是否为受支持的压缩包（单文件或分卷入口）。
// 分卷入口（任意卷）也返回 true，使前端可任选一卷发起解压。
func IsSupportedArchive(path string) bool {
	if Detect(path) != KindUnknown {
		return true
	}
	if _, _, _, ok := splitVolumeInfo(filepath.Base(path)); ok {
		return true
	}
	return false
}

// SplitSetKey 返回路径所属分卷集的去重 key（目录 + 逻辑基名），用于创建任务阶段对
// 同一分卷集（任意卷入口）去重，避免勾选多个分卷生成多条任务后互相失败。
// 仅按后缀识别、不查询文件系统（与前端“后缀名匹配”口径一致、零开销）：
//   - PKWARE 卷 name.zNN / 7-Zip 卷 name.zip.NNN：由 splitVolumeInfo 取基名；
//   - 普通单文件 .zip 也按基名纳入 key，从而与 PKWARE 末卷 name.zip 自然合并，
//     又不会误伤其它不同名的 .zip（基名不同则 key 不同）。
//
// 非归档类（文件夹 / 其它后缀）返回 ("", false)。
func SplitSetKey(path string) (string, bool) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if base, _, _, ok := splitVolumeInfo(name); ok {
		return filepath.Join(dir, base), true
	}
	lower := strings.ToLower(name)
	// .zip / .rar 单文件也纳入 key，使其与同名的 PKWARE / RAR 分卷合并为同一集合
	if strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".rar") {
		if b := strings.TrimSuffix(name, filepath.Ext(name)); b != "" {
			return filepath.Join(dir, b), true
		}
	}
	return "", false
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
// 其内容直接并入 targetDir（避免双层嵌套）。passwords 用于加密归档的轮询尝试。
// 路由策略：分卷（含 PKWARE 末卷）与 7z 走 extractSniff——打开（必要时拼接分卷）后
// 读头部魔数决定解码器，从而不依赖扩展名识别格式；其余单文件按扩展名走对应解压器。
func Extract(ctx context.Context, src, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	vols, _, isSplit, verr := EnumerateVolumes(src)
	if verr != nil {
		return verr
	}
	if isSplit {
		// RAR 分卷的每一卷都自带归档头，不能像 zip/7z 那样按字节拼接；
		// 以首卷为入口交给 rardecode，由它按命名规则自行续卷。
		if len(vols) > 0 && DetectMagic(vols[0]) == KindRar {
			return extractRarVolumes(ctx, vols, targetDir, passwords, limits, p)
		}
		return extractSniff(ctx, src, targetDir, passwords, limits, p)
	}
	// 扩展名可能被误标（如把 rar/7z 命名成 .zip），文件头魔数更可信。
	// zip / 7z / rar 的魔数唯一且与其它格式无歧义，命中即优先按魔数路由；
	// gzip 与 .tar.gz 存在歧义（同一魔数），故 gzip 不参与覆盖，仍按扩展名路由。
	switch DetectMagic(src) {
	case KindZip:
		return extractZipPath(ctx, src, targetDir, passwords, limits, p)
	case KindSevenZip:
		return extractSniff(ctx, src, targetDir, passwords, limits, p)
	case KindRar:
		return extractRar(ctx, src, targetDir, passwords, limits, p)
	}
	switch Detect(src) {
	case KindZip:
		return extractZipPath(ctx, src, targetDir, passwords, limits, p)
	case KindSevenZip:
		return extractSniff(ctx, src, targetDir, passwords, limits, p)
	case KindRar:
		return extractRar(ctx, src, targetDir, passwords, limits, p)
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
