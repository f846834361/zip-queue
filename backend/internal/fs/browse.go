package fs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zip-queue/internal/archive"
)

// Entry 描述文件浏览器中的一条目录项。
type Entry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	IsDir     bool      `json:"is_dir"`
	IsArchive bool      `json:"is_archive"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
}

// List 列出给定目录的子项（目录在前，按名称不区分大小写排序）。
// 跳过本服务产生的临时目录（前缀 .zq-tmp-）。
func List(path string) ([]Entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &NotDirError{Path: path}
	}
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if strings.HasPrefix(name, ".zq-tmp-") {
			continue
		}
		fi, err := de.Info()
		var size int64
		var mt time.Time
		if err == nil {
			size = fi.Size()
			mt = fi.ModTime()
		}
		full := filepath.Join(path, name)
		out = append(out, Entry{
			Name:      name,
			Path:      full,
			IsDir:     de.IsDir(),
			IsArchive: !de.IsDir() && archive.IsSupportedArchive(name),
			Size:      size,
			ModTime:   mt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// NotDirError 在给定路径不是目录时返回。
type NotDirError struct{ Path string }

func (e *NotDirError) Error() string { return "not a directory: " + e.Path }
