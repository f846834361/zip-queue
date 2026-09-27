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

// extractZip 解压 zip 到 targetDir，应用智能合并；支持加密 zip（按密码列表顺序尝试）。
func extractZip(ctx context.Context, src, targetDir string, passwords []string, limits Limits, p ProgressFn) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	zr, closer, totalSize, err := openZipArchive(src)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer closer.Close()

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

// EnumerateVolumes 给定分卷入口（任意一卷，或单文件 .zip），返回该归档的全部卷路径、
// 逻辑名、是否为分卷。逻辑名用于推导解压目标文件夹名与运行锁 key；任一分卷缺失或不连续时返回错误。
//   - PKWARE：base.z01, base.z02, …, base.zip（末卷为 .zip）
//   - 7-Zip ：base.zip.001, base.zip.002, …, base.zip.NNN（末卷不带 .zip）
// 入口可以是任意一卷：由后缀反推逻辑基名 base，再扫描同目录相邻分卷还原全套。
func EnumerateVolumes(src string) (vols []string, logicalName string, isSplit bool, err error) {
	dir := filepath.Dir(src)
	name := filepath.Base(src)
	base, _, kind, ok := splitVolumeInfo(name)
	if ok {
		return enumerateSplit(dir, base, kind)
	}
	// 非分卷后缀：可能是单文件 .zip，或是 PKWARE 末卷 base.zip（存在 base.z01 兄弟）
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".zip") {
		b := strings.TrimSuffix(name, ".zip")
		if _, serr := os.Stat(filepath.Join(dir, b+".z01")); serr == nil {
			return enumerateSplit(dir, b, "pkware")
		}
		return []string{src}, b, false, nil
	}
	// 理论不可达：调用方已用 Detect 保证为 zip
	return []string{src}, strings.TrimSuffix(name, filepath.Ext(name)), false, nil
}

// enumerateSplit 收集某一分卷集的全部卷（按序号升序），并校验连续性，缺口即报错。
func enumerateSplit(dir, base, kind string) (vols []string, logicalName string, isSplit bool, err error) {
	if kind == "7z" {
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
	// PKWARE：base.z01 … base.z(N) + 末卷 base.zip
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

// openZipArchive 打开 zip（单文件或分卷），返回 *zip.Reader、关闭函数与总字节数。
// 分卷通过 splitReaderAt 跨多个卷文件零拷贝拼接，无需复制成临时大文件。
func openZipArchive(src string) (*zip.Reader, io.Closer, int64, error) {
	vols, _, isSplit, err := EnumerateVolumes(src)
	if err != nil {
		return nil, nil, 0, err
	}
	if !isSplit {
		rc, err := zip.OpenReader(src)
		if err != nil {
			return nil, nil, 0, err
		}
		var sz int64
		if fi, serr := os.Stat(src); serr == nil {
			sz = fi.Size()
		}
		return &rc.Reader, rc, sz, nil
	}
	ra, total, closer, err := newSplitReaderAt(vols)
	if err != nil {
		return nil, nil, 0, err
	}
	zr, err := zip.NewReader(ra, total)
	if err != nil {
		_ = closer.Close()
		return nil, nil, 0, err
	}
	return zr, closer, total, nil
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
