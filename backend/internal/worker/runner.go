package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"zip-queue/internal/archive"
	"zip-queue/internal/model"
)

// loadPasswords 按 sort_order 顺序加载全部已启用密码的值（停用密码跳过）。
func loadPasswords(db *gorm.DB) []string {
	var pws []model.Password
	if err := db.Where("enabled = ?", true).Order("sort_order asc, id asc").Find(&pws).Error; err != nil {
		return nil
	}
	out := make([]string, 0, len(pws))
	for _, p := range pws {
		out = append(out, p.Value)
	}
	return out
}

// Runner 执行单个任务（解压或压缩）的全部副作用：临时目录、进度落库、替换原文件、清理。
type Runner struct {
	db *gorm.DB
}

func NewRunner(db *gorm.DB) *Runner { return &Runner{db: db} }

// Run 执行一个已处于 running 状态的任务。
func (r *Runner) Run(ctx context.Context, task *model.Task) {
	switch task.Type {
	case model.TypeDecompress:
		r.runDecompress(ctx, task)
	case model.TypeCompress:
		r.runCompress(ctx, task)
	default:
		r.fail(task, "unknown task type: "+task.Type)
	}
}

func (r *Runner) runDecompress(ctx context.Context, task *model.Task) {
	src := task.SourcePath

	// 源文件存在性检查
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			r.fail(task, "源压缩包不存在或已被删除："+src)
		} else {
			r.fail(task, "无法访问源文件："+classifyError(err))
		}
		return
	}

	kind := archive.Detect(src)
	if kind == archive.KindUnknown {
		r.fail(task, classifyError(archive.ErrUnsupportedFormat))
		return
	}
	dir := filepath.Dir(src)
	base := stripArchiveExt(filepath.Base(src))
	if base == "" {
		base = "decompressed"
	}
	target := filepath.Join(dir, base)
	isGzipSingle := kind == archive.KindGzip

	if _, err := os.Stat(target); err == nil {
		r.fail(task, classifyTargetExists(target, false))
		return
	}

	tempDir := filepath.Join(dir, fmt.Sprintf(".zq-tmp-%d", task.ID))
	_ = os.RemoveAll(tempDir)
	r.setTempTarget(task, tempDir, target)

	if err := archive.Extract(ctx, src, tempDir, loadPasswords(r.db), r.progressFn(task)); err != nil {
		_ = os.RemoveAll(tempDir)
		if ctx.Err() != nil {
			r.fail(task, classifyError(ctx.Err()))
			return
		}
		r.fail(task, classifyError(err))
		return
	}

	if isGzipSingle {
		inner := filepath.Join(tempDir, base)
		if err := os.Rename(inner, target); err != nil {
			_ = os.RemoveAll(tempDir)
			r.fail(task, classifyMoveError(err))
			return
		}
		_ = os.RemoveAll(tempDir)
	} else {
		if err := os.Rename(tempDir, target); err != nil {
			_ = os.RemoveAll(tempDir)
			r.fail(task, classifyMoveError(err))
			return
		}
	}

	if err := os.Remove(src); err != nil {
		r.fail(task, classifyDeleteError(err, model.TypeDecompress))
		return
	}
	r.succeed(task)
}

func (r *Runner) runCompress(ctx context.Context, task *model.Task) {
	src := task.SourcePath
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			r.fail(task, "源文件/文件夹不存在或已被删除："+src)
		} else {
			r.fail(task, "无法访问源文件："+classifyError(err))
		}
		return
	}
	dir := filepath.Dir(src)
	base := filepath.Base(src)
	if !info.IsDir() {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	target := filepath.Join(dir, base+".zip")
	if _, err := os.Stat(target); err == nil {
		r.fail(task, classifyTargetExists(target, true))
		return
	}
	tempZip := filepath.Join(dir, fmt.Sprintf(".zq-tmp-%d.zip", task.ID))
	_ = os.RemoveAll(tempZip)
	r.setTempTarget(task, tempZip, target)

	if err := archive.Compress(ctx, src, tempZip, r.progressFn(task)); err != nil {
		_ = os.RemoveAll(tempZip)
		if ctx.Err() != nil {
			r.fail(task, classifyError(ctx.Err()))
			return
		}
		r.fail(task, classifyError(err))
		return
	}
	if err := os.Rename(tempZip, target); err != nil {
		_ = os.RemoveAll(tempZip)
		r.fail(task, classifyMoveError(err))
		return
	}
	if err := os.RemoveAll(src); err != nil {
		r.fail(task, classifyDeleteError(err, model.TypeCompress))
		return
	}
	r.succeed(task)
}

// progressFn 返回节流的进度回调：archive.tracker 每 256ms 调用一次。
func (r *Runner) progressFn(task *model.Task) archive.ProgressFn {
	return func(p archive.Progress) {
		updates := map[string]interface{}{
			"processed_bytes":   p.ProcessedBytes,
			"total_bytes":       p.TotalBytes,
			"processed_entries": p.ProcessedEntries,
			"total_entries":     p.TotalEntries,
			"current_entry":     p.CurrentEntry,
		}
		if pct := p.Percent(); pct >= 0 {
			updates["progress_percent"] = pct
		}
		r.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates)
	}
}

func (r *Runner) setTempTarget(task *model.Task, temp, target string) {
	task.TempPath = temp
	task.TargetPath = target
	r.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"temp_path":   temp,
		"target_path": target,
	})
}

func (r *Runner) fail(task *model.Task, msg string) {
	now := time.Now()
	r.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status":       model.StatusFailed,
		"error":        truncate(msg, 2048),
		"completed_at": now,
		"temp_path":    "",
	})
}

func (r *Runner) succeed(task *model.Task) {
	now := time.Now()
	r.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status":           model.StatusSucceeded,
		"progress_percent": 100,
		"completed_at":     now,
		"temp_path":        "",
		"error":            "",
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// stripArchiveExt 去除压缩包扩展名（长后缀优先）。
func stripArchiveExt(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range []string{".tar.gz", ".tgz", ".tar", ".zip", ".gz"} {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}
