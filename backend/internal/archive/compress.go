package archive

import (
	"archive/zip"
	"compress/flate"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Level 是压缩效率档位：越"慢"压缩率越高，CPU 开销也越大。
// 使用标准库 archive/zip 写入（按 Writer 注册 Deflate 级别），
// 解压仍由 github.com/alexmullins/zip 完成（需要加密 zip 支持）。
type Level string

const (
	// LevelFastest 特快：仅打包不压缩（zip.Store），速度最快、产物最大。
	LevelFastest Level = "fastest"
	// LevelFast 快：Deflate 最低压缩级别。
	LevelFast Level = "fast"
	// LevelNormal 中：Deflate 默认级别，未设置时的回落值。
	LevelNormal Level = "normal"
	// LevelSlow 慢：Deflate 最高压缩级别，压缩率最高、耗时最长。
	LevelSlow Level = "slow"
)

// ParseLevel 解析压缩效率字符串；未知或空值回落 LevelNormal。
func ParseLevel(s string) Level {
	switch Level(s) {
	case LevelFastest, LevelFast, LevelNormal, LevelSlow:
		return Level(s)
	}
	return LevelNormal
}

// Valid 判断档位是否合法。
func (l Level) Valid() bool {
	switch l {
	case LevelFastest, LevelFast, LevelNormal, LevelSlow:
		return true
	}
	return false
}

// Method 返回该档位使用的 zip 压缩方法：特快为 Store（仅打包），其余为 Deflate。
func (l Level) Method() uint16 {
	if l == LevelFastest {
		return zip.Store
	}
	return zip.Deflate
}

// FlateLevel 返回 Deflate 压缩级别，仅在 Method 为 Deflate 时有意义。
func (l Level) FlateLevel() int {
	switch l {
	case LevelFast:
		return flate.BestSpeed // 1
	case LevelSlow:
		return flate.BestCompression // 9
	default:
		return flate.DefaultCompression // -1，即级别 6
	}
}

// Compress 将 src（文件夹或单文件）打包为 destZip（zip）。
// level 决定压缩效率：LevelFastest 仅打包不压缩，其余为不同级别的 Deflate。
// stripFolder 为 true 时采用修复 commit 2f948182a 之前的逻辑：zip 内不保留
// 被选中的顶层文件夹这一层（条目退化成 a.txt / sub/b.txt）；false（默认）保留
// 顶层目录（MyFolder/a.txt），与 PC 上「右键文件夹 → 压缩」一致。
func Compress(ctx context.Context, src, destZip string, level Level, stripFolder bool, p ProgressFn) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	var files []string
	var totalBytes int64
	var basePath string
	if info.IsDir() {
		if stripFolder {
			// 去掉文件夹：basePath 取 src 本身，zip 内条目相对 src 计算，丢失顶层目录。
			basePath = src
		} else {
			// 保留顶层文件夹：basePath 取父目录，条目相对父目录计算（如 MyFolder/a.txt）。
			basePath = filepath.Dir(src)
		}
		walkErr := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			files = append(files, path)
			totalBytes += info.Size()
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	} else {
		basePath = filepath.Dir(src)
		files = []string{src}
		totalBytes = info.Size()
	}
	if len(files) == 0 {
		return fmt.Errorf("no files to compress in %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(destZip), 0o755); err != nil {
		return err
	}
	out, err := os.Create(destZip)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	// 按 Writer 实例注册 Deflate 压缩器以指定压缩级别（包级 RegisterCompressor
	// 无法覆盖内置的 Deflate，会 panic）。特快档用 Store，无需注册。
	if method := level.Method(); method == zip.Deflate {
		flateLevel := level.FlateLevel()
		zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(w, flateLevel)
		})
	}
	// 兜底关闭：保证任意失败路径都释放句柄（Windows 上未关闭的句柄会阻止临时文件清理）。
	// 成功路径在函数尾部已显式 Close 并检查错误，此处重复 Close 的错误可忽略。
	defer func() {
		_ = zw.Close()
		_ = out.Close()
	}()

	t := newTracker(totalBytes, p)
	for _, fpath := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(basePath, fpath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Stat(fpath)
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = rel
		h.Method = level.Method()
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(fpath)
		if err != nil {
			return err
		}
		cw := &countWriter{w: w, t: t}
		_, copyErr := copyWithLimit(ctx, cw, f, -1)
		f.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	t.notify(true)
	// 依次关闭 zip writer 与文件句柄：Close 负责 flush deflate 缓冲并写入
	// central directory，错误不可忽略，否则磁盘满时截断的 zip 会被判成功。
	if err := zw.Close(); err != nil {
		return fmt.Errorf("write zip: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close zip file: %w", err)
	}
	return nil
}
