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
// 返回目录项列表、该目录自身的最后修改时间，以及可能的错误。
func List(path string) ([]Entry, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !info.IsDir() {
		return nil, time.Time{}, &NotDirError{Path: path}
	}
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	out := make([]Entry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if strings.HasPrefix(name, ".zq-tmp-") {
			continue
		}
		full := filepath.Join(path, name)
		e, err := buildEntry(full)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	sortEntries(out)
	return out, info.ModTime(), nil
}

// buildEntry 由完整路径构造一个目录项（单次 Lstat，廉价，且不跟随符号链接，
// 与 os.ReadDir 得到的 DirEntry.Info() 语义一致）。既用于 List 全量遍历，
// 也用于任务完成后对缓存条目的局部增量更新（此时只有单条路径，没有 DirEntry）。
func buildEntry(full string) (Entry, error) {
	fi, err := os.Lstat(full)
	if err != nil {
		return Entry{}, err
	}
	name := filepath.Base(full)
	return Entry{
		Name:      name,
		Path:      full,
		IsDir:     fi.IsDir(),
		IsArchive: !fi.IsDir() && archive.IsSupportedArchive(name),
		Size:      fi.Size(),
		ModTime:   fi.ModTime(),
	}, nil
}

// sortEntries 按「目录在前、名称不区分大小写」对目录项原地排序，保证输出确定性顺序。
func sortEntries(out []Entry) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
}

// NotDirError 在给定路径不是目录时返回。
type NotDirError struct{ Path string }

func (e *NotDirError) Error() string { return "not a directory: " + e.Path }
