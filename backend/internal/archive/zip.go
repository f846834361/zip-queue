package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	zip "github.com/alexmullins/zip"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// ErrPasswordRequired 当 zip 加密但无匹配密码时返回。
var ErrPasswordRequired = fmt.Errorf("zip is encrypted but no matching password found")

// extractZip 从已打开的 *zip.Reader 解压（单文件与分卷共用；分卷由调用方拼接后传入）。
// 应用智能合并；支持加密 zip（按密码列表顺序尝试）。
func extractZip(ctx context.Context, zr *zip.Reader, totalSize int64, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}

	// 探测是否有加密条目，若有则按顺序轮询密码（UTF-8 失败回退 GBK）
	password := ""
	passwordGBK := false
	for _, f := range zr.File {
		if !f.IsEncrypted() {
			continue
		}
		// 找到第一个加密条目，用其探测正确密码
		pw, gbk, err := tryPasswords(zr, passwords)
		if err != nil {
			return err
		}
		password = pw
		passwordGBK = gbk
		break
	}

	// 扫描条目以确定剥离前缀与总量
	var entries []entryMeta
	for _, f := range zr.File {
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
	// 压缩率限制：解压后估算大小 / 原压缩包大小 超过阈值即视为解压炸弹。
	// 分卷下改用全部分卷总大小 totalSize 参与比率计算（单文件时 totalSize 即该文件大小）。
	if limits.MaxRatio > 0 && totalSize > 0 {
		ratio := float64(totalBytes) / float64(totalSize)
		if ratio > float64(limits.MaxRatio) {
			return fmt.Errorf("%w：压缩率 %.0f 超过上限 %d（解压后约 %d 字节 / 压缩包 %d 字节）",
				ErrLimitExceeded, ratio, limits.MaxRatio, totalBytes, totalSize)
		}
	}
	t := newTracker(totalBytes, p)

	for _, f := range zr.File {
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
func tryPasswords(zr *zip.Reader, passwords []string) (string, bool, error) {
	if len(passwords) == 0 {
		return "", false, ErrPasswordRequired
	}
	// 找到第一个加密的非目录条目作为探针
	var probe *zip.File
	for _, f := range zr.File {
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

// EnumerateVolumes 给定分卷入口（任意一卷，或单文件归档），返回该归档的全部卷路径、
// 逻辑名、是否为分卷。逻辑名用于推导解压目标文件夹名与运行锁 key；任一分卷缺失或不连续时返回错误。
//
// 关键设计：分卷查找严格按真实压缩类型分发，不使用“通用数字后缀”兜底——否则会出现
// 把无关的数字后缀文件（如 name.001）误认为某归档的分卷。流程如下：
//   1. 先用文件头魔数（扩展名不可靠）判定真实类型 Kind，未知时回退扩展名；
//   2. 入口命名像分卷时，校验其分卷命名类型与真实类型相符，相符才展开；
//   3. 入口是单文件命名时，按真实类型去同目录查找对应的兄弟卷命名。
//
// 各类型分卷命名：
//   - zip  ：PKWARE 风格 base.z01…base.z(N) + 末卷 base.zip；或数字分卷 base.zip.001…
//   - 7z   ：数字分卷 base.7z.001, base.7z.002, …
//   - rar  ：新式 base.partN.rar；旧式 base.rar（首卷）+ base.r00, base.r01, …
func EnumerateVolumes(src string) (vols []string, logicalName string, isSplit bool, err error) {
	dir := filepath.Dir(src)
	name := filepath.Base(src)

	// 真实压缩类型优先（魔数比扩展名可靠，扩展名可能是误标）；未知时回退扩展名
	at := DetectMagic(src)
	if at == KindUnknown {
		at = Detect(src)
	}

	// 1) 入口命名像分卷：解析基名。强命名分卷（带容器扩展名或专属后缀）直接按命名带出的
	//    类型展开；仅“无容器扩展名的纯数字分卷”（如 name.001）需要真实类型兜底，避免把
	//    无关的数字后缀文件误当分卷。
	if base, _, nameKind, ok := splitVolumeInfo(name); ok {
		if nameType := nameKindToType(nameKind, base); nameType != KindUnknown {
			return enumerateSplit(dir, base, nameType)
		}
		if at != KindUnknown {
			return enumerateSplit(dir, base, at)
		}
		return []string{src}, strings.TrimSuffix(name, filepath.Ext(name)), false, nil
	}

	// 2) 入口是单文件命名：按真实类型查找同目录兄弟卷
	switch at {
	case KindZip:
		b := strings.TrimSuffix(name, ".zip")
		if _, serr := os.Stat(filepath.Join(dir, b+".z01")); serr == nil {
			return enumerateSplit(dir, b, KindZip)
		}
		if m, _ := filepath.Glob(filepath.Join(dir, b+".zip.[0-9]*")); len(m) > 0 {
			return enumerateSplit(dir, b, KindZip)
		}
	case KindSevenZip:
		b := strings.TrimSuffix(name, ".7z")
		if m, _ := filepath.Glob(filepath.Join(dir, b+".7z.[0-9]*")); len(m) > 0 {
			return enumerateSplit(dir, b, KindSevenZip)
		}
	case KindRar:
		b := strings.TrimSuffix(name, ".rar")
		if m, _ := filepath.Glob(filepath.Join(dir, b+".r00")); len(m) > 0 {
			return enumerateSplit(dir, b, KindRar)
		}
		if m, _ := filepath.Glob(filepath.Join(dir, b+".part*.rar")); len(m) > 0 {
			return enumerateSplit(dir, b, KindRar)
		}
	}
	return []string{src}, strings.TrimSuffix(name, filepath.Ext(name)), false, nil
}

// nameKindToType 把分卷命名建议类型换算为真实压缩类型。
// 强命名（pkware / rar 专属后缀）直接确定类型；容器扩展名数字分卷（name.zip.001 等）
// 由扩展名确定；无容器扩展名的纯数字分卷返回 unknown，交由调用方用真实类型兜底。
func nameKindToType(nameKind, base string) Kind {
	switch nameKind {
	case "pkware":
		return KindZip
	case "rarmulti", "rarold":
		return KindRar
	case "split":
		return containerExtKind(base)
	}
	return KindUnknown
}

// containerExtKind 取 base 的末扩展名对应的压缩类型（仅 zip/7z/rar 支持分卷）。
func containerExtKind(base string) Kind {
	switch strings.ToLower(filepath.Ext(base)) {
	case ".zip":
		return KindZip
	case ".7z":
		return KindSevenZip
	case ".rar":
		return KindRar
	}
	return KindUnknown
}

// enumerateSplit 按真实压缩类型 at 分发到对应分卷枚举实现。
func enumerateSplit(dir, base string, at Kind) (vols []string, logicalName string, isSplit bool, err error) {
	switch at {
	case KindZip:
		return enumerateZipVolumes(dir, base)
	case KindSevenZip:
		return enumerateSevenZipVolumes(dir, base)
	case KindRar:
		return enumerateRarVolumes(dir, base)
	}
	return nil, "", true, fmt.Errorf("暂不支持该类型的分卷解压：%s", at)
}

// enumerateNumericVolumes 收集 base.NNN 数字分卷（base.zip.001 / base.7z.001 等），按序号校验连续性。
func enumerateNumericVolumes(dir, base string) (vols []string, logicalName string, isSplit bool, err error) {
	matches, _ := filepath.Glob(filepath.Join(dir, base+".*"))
	idx := map[int]string{}
	maxIdx := 0
	for _, m := range matches {
		suf := strings.TrimPrefix(filepath.Base(m), base+".")
		if !isAllDigits(suf) {
			continue
		}
		n, _ := strconv.Atoi(suf)
		idx[n] = m
		if n > maxIdx {
			maxIdx = n
		}
	}
	if maxIdx == 0 {
		return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, base+".001"))
	}
	for n := 1; n <= maxIdx; n++ {
		if _, ok := idx[n]; !ok {
			return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, fmt.Sprintf("%s.%03d", base, n)))
		}
	}
	vols = make([]string, 0, maxIdx)
	for n := 1; n <= maxIdx; n++ {
		vols = append(vols, idx[n])
	}
	return vols, base, true, nil
}

// enumerateZipVolumes 收集 zip 分卷：PKWARE（base.zNN + base.zip）或数字分卷（base.zip.NNN）。
func enumerateZipVolumes(dir, base string) (vols []string, logicalName string, isSplit bool, err error) {
	if strings.HasSuffix(base, ".zip") {
		// 数字分卷风格：name.zip.001, name.zip.002, …
		return enumerateNumericVolumes(dir, base)
	}
	// PKWARE 风格：base.z01 … base.z(N) + 末卷 base.zip
	matches, _ := filepath.Glob(filepath.Join(dir, base+".z*"))
	idx := map[int]string{}
	maxIdx := 0
	for _, m := range matches {
		suf := strings.TrimPrefix(filepath.Base(m), base+".z")
		if !isAllDigits(suf) {
			continue
		}
		n, _ := strconv.Atoi(suf)
		idx[n] = m
		if n > maxIdx {
			maxIdx = n
		}
	}
	last := filepath.Join(dir, base+".zip")
	if _, serr := os.Stat(last); serr != nil {
		return nil, "", true, fmt.Errorf("分卷缺失：%s", last)
	}
	if maxIdx == 0 {
		return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, base+".z01"))
	}
	for n := 1; n <= maxIdx; n++ {
		if _, ok := idx[n]; !ok {
			return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, fmt.Sprintf("%s.z%02d", base, n)))
		}
	}
	vols = make([]string, 0, maxIdx+1)
	for n := 1; n <= maxIdx; n++ {
		vols = append(vols, idx[n])
	}
	vols = append(vols, last)
	return vols, base, true, nil
}

// enumerateSevenZipVolumes 收集 7z 数字分卷：base.7z.001, base.7z.002, …
func enumerateSevenZipVolumes(dir, base string) (vols []string, logicalName string, isSplit bool, err error) {
	return enumerateNumericVolumes(dir, base)
}

// enumerateRarVolumes 收集 RAR 分卷：优先新式 base.partN.rar，否则旧式 base.rar + base.rNN。
func enumerateRarVolumes(dir, base string) (vols []string, logicalName string, isSplit bool, err error) {
	if m, _ := filepath.Glob(filepath.Join(dir, base+".part*.rar")); len(m) > 0 {
		return enumerateRarMulti(dir, base)
	}
	return enumerateRarOld(dir, base)
}

// enumerateRarMulti 收集 RAR 新式分卷：base.part1.rar, base.part2.rar, …（序号从 1 开始）。
func enumerateRarMulti(dir, base string) (vols []string, logicalName string, isSplit bool, err error) {
	matches, _ := filepath.Glob(filepath.Join(dir, base+".part*.rar"))
	idx := map[int]string{}
	maxIdx := 0
	lbase := strings.ToLower(base)
	for _, m := range matches {
		lb := strings.ToLower(filepath.Base(m))
		if !strings.HasPrefix(lb, lbase+".part") || !strings.HasSuffix(lb, ".rar") {
			continue
		}
		mid := lb[len(lbase)+len(".part") : len(lb)-len(".rar")]
		if i := strings.Index(mid, "of"); i > 0 { // 兼容 partNofM 命名（如 part1of5.rar）
			mid = mid[:i]
		}
		if !isAllDigits(mid) {
			continue
		}
		n, _ := strconv.Atoi(mid)
		idx[n] = m
		if n > maxIdx {
			maxIdx = n
		}
	}
	if maxIdx == 0 {
		return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, base+".part1.rar"))
	}
	for n := 1; n <= maxIdx; n++ {
		if _, ok := idx[n]; !ok {
			return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, fmt.Sprintf("%s.part%d.rar", base, n)))
		}
	}
	vols = make([]string, 0, maxIdx)
	for n := 1; n <= maxIdx; n++ {
		vols = append(vols, idx[n])
	}
	return vols, base, true, nil
}

// enumerateRarOld 收集 RAR 旧式分卷：base.rar（首卷） + base.r00, base.r01, …
func enumerateRarOld(dir, base string) (vols []string, logicalName string, isSplit bool, err error) {
	first, _ := filepath.Glob(filepath.Join(dir, base+".rar"))
	if len(first) == 0 {
		return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, base+".rar"))
	}
	matches, _ := filepath.Glob(filepath.Join(dir, base+".r*"))
	idx := map[int]string{}
	maxIdx := 0
	lbase := strings.ToLower(base)
	for _, m := range matches {
		lb := strings.ToLower(filepath.Base(m))
		if !strings.HasPrefix(lb, lbase+".r") {
			continue
		}
		suf := lb[len(lbase)+len(".r"):]
		if len(suf) < 2 || !isAllDigits(suf) {
			continue // 排除首卷 .rar
		}
		n, _ := strconv.Atoi(suf)
		idx[n+1] = m
		if n+1 > maxIdx {
			maxIdx = n + 1
		}
	}
	if maxIdx == 0 {
		return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, base+".r00"))
	}
	for n := 1; n <= maxIdx; n++ {
		if _, ok := idx[n]; !ok {
			return nil, "", true, fmt.Errorf("分卷缺失：%s", filepath.Join(dir, fmt.Sprintf("%s.r%02d", base, n-1)))
		}
	}
	vols = make([]string, 0, maxIdx+1)
	vols = append(vols, first[0])
	for n := 1; n <= maxIdx; n++ {
		vols = append(vols, idx[n])
	}
	return vols, base, true, nil
}

// extractZipPath 单文件 .zip（非分卷）入口：打开后委托 extractZip。
func extractZipPath(ctx context.Context, src, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	rc, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer rc.Close()
	var sz int64
	if fi, serr := os.Stat(src); serr == nil {
		sz = fi.Size()
	}
	return extractZip(ctx, &rc.Reader, sz, targetDir, passwords, limits, p)
}

// openRaw 打开归档（单文件或分卷），返回可随机读接口、总字节数与关闭函数。
// 分卷通过 splitReaderAt 跨多文件零拷贝拼接；单文件直接以 *os.File 作为 ReaderAt。
func openRaw(src string) (io.ReaderAt, int64, io.Closer, error) {
	vols, _, isSplit, err := EnumerateVolumes(src)
	if err != nil {
		return nil, 0, nil, err
	}
	if !isSplit {
		f, err := os.Open(src)
		if err != nil {
			return nil, 0, nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, 0, nil, err
		}
		return f, fi.Size(), f, nil
	}
	return newSplitReaderAt(vols)
}

// extractSniff 打开归档（必要时拼接分卷）后读取头部魔数，按格式路由到对应解压器。
// 这样分卷与单文件、zip 与 7z 都能用同一入口，且识别不依赖文件扩展名。
func extractSniff(ctx context.Context, src, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	ra, total, closer, err := openRaw(src)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer closer.Close()
	buf := make([]byte, 8)
	if n, e := ra.ReadAt(buf, 0); n == 0 && e != nil {
		return fmt.Errorf("read header: %w", e)
	}
	switch {
	case isZipMagic(buf):
		zr, e := zip.NewReader(ra, total)
		if e != nil {
			return fmt.Errorf("open zip: %w", e)
		}
		return extractZip(ctx, zr, total, targetDir, passwords, limits, p)
	case is7zMagic(buf):
		return extract7z(ctx, ra, total, targetDir, passwords, limits, p)
	default:
		return ErrUnsupportedFormat
	}
}

// isZipMagic 判断是否为 ZIP 本地文件头 / 中央目录尾（PK\x03\x04 / PK\x05\x06）。
func isZipMagic(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' &&
		(b[2] == 0x03 || b[2] == 0x05) && (b[3] == 0x04 || b[3] == 0x06)
}

// is7zMagic 判断是否为 7z 签名（7z¼¯'）。
func is7zMagic(b []byte) bool {
	return len(b) >= 6 && b[0] == '7' && b[1] == 'z' &&
		b[2] == 0xBC && b[3] == 0xAF && b[4] == 0x27 && b[5] == 0x1C
}

// splitReaderAt 实现 io.ReaderAt，跨多个分卷文件按拼接偏移随机寻址读取，
// 从而把分卷集零拷贝还原为完整 zip 流交给 zip.NewReader 解析中央目录与条目。
// 仅分卷时多开若干只读句柄并 Stat 计算累计偏移，无额外内存/磁盘拷贝。
type splitReaderAt struct {
	files   []*os.File
	offsets []int64 // 长度 = len(files)+1；offsets[i] 为第 i 个文件的起始偏移，末项为总大小
}

func newSplitReaderAt(vols []string) (*splitReaderAt, int64, io.Closer, error) {
	s := &splitReaderAt{}
	var off int64
	for _, v := range vols {
		f, err := os.Open(v)
		if err != nil {
			s.Close()
			return nil, 0, nil, fmt.Errorf("打开分卷 %s 失败：%w", v, err)
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			s.Close()
			return nil, 0, nil, err
		}
		s.offsets = append(s.offsets, off)
		s.files = append(s.files, f)
		off += fi.Size()
	}
	s.offsets = append(s.offsets, off)
	return s, off, s, nil
}

func (s *splitReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > s.offsets[len(s.offsets)-1] {
		return 0, errors.New("split: read offset out of range")
	}
	// 定位包含 off 的卷：offsets[k] <= off < offsets[k+1]
	k := sort.Search(len(s.offsets)-1, func(i int) bool { return s.offsets[i+1] > off })
	var total int
	for total < len(p) && k < len(s.files) {
		local := off - s.offsets[k]
		n, rerr := s.files[k].ReadAt(p[total:], local)
		total += n
		off += int64(n)
		if rerr == nil {
			if n == 0 {
				break
			}
			k++
			continue
		}
		if rerr == io.EOF {
			// 到达本卷末尾：跨入下一卷继续读（分卷可能在任意字节处切开，单个条目可跨卷）
			k++
			continue
		}
		return total, rerr
	}
	if total < len(p) {
		return total, io.EOF
	}
	return total, nil
}

func (s *splitReaderAt) Close() error {
	var err error
	for _, f := range s.files {
		if e := f.Close(); e != nil {
			err = e
		}
	}
	s.files = nil
	return err
}

// 压缩实现见 compress.go：写入改用标准库 archive/zip，以便按压缩效率档位
// 指定 Deflate 级别或仅打包（Store）；本文件只负责加密 zip 的解压。
