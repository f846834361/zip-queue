package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"zip-queue/internal/archive"
	"zip-queue/internal/fs"
	"zip-queue/internal/model"
	"zip-queue/internal/setting"
)

// TempDirPrefix 是任务执行期临时目录/临时文件名前缀。
// API 侧批量扫描目录时会跳过带此前缀的路径，避免扫到运行中任务的产物。
const TempDirPrefix = ".zq-tmp-"

// loadPasswords 按 sort_order 顺序加载全部已启用密码的值（停用密码跳过）。
func loadPasswords(db *gorm.DB) []string {
	var pws []model.Password
	if err := db.Where("enabled = ?", true).Order("sort_order asc, id asc").Find(&pws).Error; err != nil {
		log.Printf("加载密码列表失败，本次执行将无法解密加密包：%v", err)
		return nil
	}
	out := make([]string, 0, len(pws))
	for _, p := range pws {
		out = append(out, p.Value)
	}
	return out
}

// loadCompressionLevel 读取压缩效率档位（任务开始时取一次，未设置回落默认档）。
// 与密码同样在任务执行时读库，使页面改配置只影响之后开始的任务。
func loadCompressionLevel(db *gorm.DB) archive.Level {
	return archive.ParseLevel(setting.Get(db, setting.KeyCompressionLevel))
}

// loadStripFolder 读取"去掉顶层文件夹"开关（任务开始时取一次，默认 false）。
// 与压缩效率同样执行期读库，使页面改配置只影响之后开始的任务。
func loadStripFolder(db *gorm.DB) bool {
	v, err := strconv.ParseBool(setting.Get(db, setting.KeyStripFolder))
	return err == nil && v
}

// loadAddFolderMode 读取"智能添加文件夹"模式（任务开始时取一次，未设置回落默认 1个）。
// 与压缩效率同样执行期读库，使页面改配置只影响之后开始的任务。
func loadAddFolderMode(db *gorm.DB) string {
	v := setting.Get(db, setting.KeyAddFolderMode)
	if !setting.ValidAddFolderMode(v) {
		return setting.AddFolderNone
	}
	return v
}

// shouldWrapFolder 按"智能添加文件夹"模式，结合解压出的顶层条目决定要不要包一层父文件夹。
//   - none：始终不包。
//   - multiple（多个）：仅当顶层条目数 > 1 才包；单个文件或单个文件夹都不包（避免文件夹嵌套）。
//   - one（1个）：多个条目必包；单个文件也包；仅当单个条目本身是文件夹时不包（避免套文件夹）。
func shouldWrapFolder(mode string, entries []os.DirEntry) bool {
	if mode == setting.AddFolderNone {
		return false
	}
	if len(entries) > 1 {
		return true
	}
	if len(entries) == 0 {
		return false
	}
	if entries[0].IsDir() {
		// 单个条目已是文件夹：包一层会变成文件夹套文件夹，故不包。
		return false
	}
	// 单个文件：one 包、multiple 不包。
	return mode == setting.AddFolderOne
}

// CancelChecker 供 Runner 判断某任务是否由用户主动取消（以区分服务中断重排）。
type CancelChecker interface {
	IsCancelled(taskID uint) bool
}

// Runner 执行单个任务（解压或压缩）的全部副作用：临时目录、进度落库、替换原文件、清理。
type Runner struct {
	db     *gorm.DB
	limits archive.Limits
	chk    CancelChecker
}

func NewRunner(db *gorm.DB, limits archive.Limits, chk CancelChecker) *Runner {
	return &Runner{db: db, limits: limits, chk: chk}
}

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
	base := archive.StripArchiveExt(filepath.Base(src))
	if base == "" {
		base = "decompressed"
	}
	// target 是"加文件夹"时的父文件夹（以 zip 名命名），包文件夹时结果落在 dir/base。
	target := filepath.Join(dir, base)
	addMode := loadAddFolderMode(r.db)

	// 目标冲突预检（在大量解压前快速失败）：加文件夹时检查 wrapper 目录；
	// 不加文件夹时条目名需解压后才知道，冲突检查放到移动前。
	if addMode != setting.AddFolderNone {
		if _, err := os.Stat(target); err == nil {
			r.fail(task, classifyTargetExists(target, false))
			return
		}
	}

	tempDir := filepath.Join(dir, fmt.Sprintf("%s%d", TempDirPrefix, task.ID))
	_ = os.RemoveAll(tempDir)
	r.setTempTarget(task, tempDir, target)

	if err := archive.Extract(ctx, src, tempDir, loadPasswords(r.db), r.limits, r.progressFn(task)); err != nil {
		if ctx.Err() != nil {
			if r.chk != nil && r.chk.IsCancelled(task.ID) {
				// 用户主动取消：清理临时文件并标记 cancelled（不重排）
				r.finishCancelled(task)
				return
			}
			// 服务停止/重启导致的中断：删除临时文件，可安全重做的补一条待执行任务
			FinishInterrupted(r.db, task)
			return
		}
		_ = os.RemoveAll(tempDir)
		r.fail(task, classifyError(err))
		return
	}

	// 解压完成：依据"智能添加文件夹"配置，决定是否把结果包一层父文件夹。
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		r.fail(task, classifyError(err))
		return
	}
	if !shouldWrapFolder(addMode, entries) {
		// 不加文件夹：把 tempDir 内顶层条目直接搬到 dir 下，并避免与已有条目冲突。
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				_ = os.RemoveAll(tempDir)
				r.fail(task, classifyTargetExists(filepath.Join(dir, e.Name()), false))
				return
			}
		}
		for _, e := range entries {
			if err := os.Rename(filepath.Join(tempDir, e.Name()), filepath.Join(dir, e.Name())); err != nil {
				_ = os.RemoveAll(tempDir)
				r.fail(task, classifyMoveError(err))
				return
			}
		}
		_ = os.RemoveAll(tempDir)
		// 记录最终落点供列表缓存增量更新：单条目回写其真实路径，多条目回写父目录。
		finalTarget := dir
		if len(entries) == 1 {
			finalTarget = filepath.Join(dir, entries[0].Name())
		}
		r.updateTask(task.ID, map[string]interface{}{"target_path": finalTarget}, "写入最终目标路径")
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
	tempZip := filepath.Join(dir, fmt.Sprintf("%s%d.zip", TempDirPrefix, task.ID))
	_ = os.RemoveAll(tempZip)
	r.setTempTarget(task, tempZip, target)

	// 任务开始执行时取一次压缩效率档位与是否去顶层文件夹（与密码同样执行期读库），
	// 并记录到任务，使任务详情可回显"当时实际使用的效率"，即使之后在配置页改动也不受影响。
	level := loadCompressionLevel(r.db)
	stripFolder := loadStripFolder(r.db)
	r.updateTask(task.ID, map[string]interface{}{"compression_level": string(level)}, "写入压缩效率档位")

	if err := archive.Compress(ctx, src, tempZip, level, stripFolder, setting.SkipCompressed(r.db), r.progressFn(task)); err != nil {
		if ctx.Err() != nil {
			if r.chk != nil && r.chk.IsCancelled(task.ID) {
				// 用户主动取消：清理临时文件并标记 cancelled（不重排）
				r.finishCancelled(task)
				return
			}
			// 服务停止/重启导致的中断：删除临时文件，可安全重做的补一条待执行任务
			FinishInterrupted(r.db, task)
			return
		}
		_ = os.RemoveAll(tempZip)
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
			"processed_bytes": p.ProcessedBytes,
			"total_bytes":     p.TotalBytes,
		}
		if pct := p.Percent(); pct >= 0 {
			updates["progress_percent"] = pct
		}
		if err := r.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
			log.Printf("任务 #%d 进度回写失败：%v", task.ID, err)
		}
	}
}

func (r *Runner) setTempTarget(task *model.Task, temp, target string) {
	task.TempPath = temp
	task.TargetPath = target
	r.updateTask(task.ID, map[string]interface{}{
		"temp_path":   temp,
		"target_path": target,
	}, "写入临时路径")
}

func (r *Runner) fail(task *model.Task, msg string) {
	r.updateTask(task.ID, map[string]interface{}{
		"status":       model.StatusFailed,
		"error":        truncate(msg, 2048),
		"completed_at": time.Now(),
		"temp_path":    "",
	}, "写入失败状态")
}

func (r *Runner) succeed(task *model.Task) {
	// 任务成功后对源目录的列表缓存做精确增量更新（删 SourcePath、加 TargetPath），
	// 而非整条失效：解压删源压缩包加解压目录、压缩删源加 .zip，delta 完全一致。
	// 这样对含大量文件的目录在频繁任务下不会反复触发昂贵的全量重列，缓存始终温热；
	// 外部/手动改动仍由 ListCached 的 mtime 守卫兜底失效。
	fs.UpdateListCache(filepath.Dir(task.SourcePath), task.SourcePath, task.TargetPath)
	r.updateTask(task.ID, map[string]interface{}{
		"status":           model.StatusSucceeded,
		"progress_percent": 100,
		"completed_at":     time.Now(),
		"temp_path":        "",
		"error":            "",
	}, "写入成功状态")
}

// finishCancelled 收尾被用户取消的任务：清理临时文件（解压临时目录或临时压缩包）、
// 标记 cancelled；不重排（用户主动取消，不应自动续做），源/目标文件均保留。
func (r *Runner) finishCancelled(task *model.Task) {
	if task.TempPath != "" {
		_ = os.RemoveAll(task.TempPath)
	}
	r.updateTask(task.ID, map[string]interface{}{
		"status":       model.StatusCancelled,
		"completed_at": time.Now(),
		"temp_path":    "",
		"error":        "任务已被用户取消",
	}, "写入取消状态")
}

// updateTask 写入任务字段，失败时重试一次并记录日志。
// 状态落库失败的任务会停留在 running，由下次重启的 RecoverOnStartup 兜底，
// 这里至少要把失败暴露到日志。
func (r *Runner) updateTask(taskID uint, updates map[string]interface{}, what string) {
	err := r.db.Model(&model.Task{}).Where("id = ?", taskID).Updates(updates).Error
	if err == nil {
		return
	}
	log.Printf("任务 #%d %s失败，重试一次：%v", taskID, what, err)
	time.Sleep(100 * time.Millisecond)
	if err := r.db.Model(&model.Task{}).Where("id = ?", taskID).Updates(updates).Error; err != nil {
		log.Printf("任务 #%d %s仍失败，重启后由启动恢复兜底：%v", taskID, what, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// 按 rune 边界回退，避免把多字节字符（如中文）切成非法 UTF-8
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
