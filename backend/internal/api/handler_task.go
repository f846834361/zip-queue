package api

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"zip-queue/internal/archive"
	"zip-queue/internal/model"
	"zip-queue/internal/worker"
)

type createTaskRequest struct {
	Type string `json:"type" binding:"required"`
	Path string `json:"path" binding:"required"`
}

// CreateTask 校验并创建单个任务（一个文件一个任务）。
func (a *API) CreateTask(c *gin.Context) {
	var req createTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Type != model.TypeDecompress && req.Type != model.TypeCompress {
		c.JSON(400, gin.H{"error": "invalid type: " + req.Type})
		return
	}
	task, reason := taskForPath(req.Type, req.Path)
	if task == nil {
		c.JSON(400, gin.H{"error": reason})
		return
	}
	if a.hasActiveTask(req.Type, req.Path) {
		c.JSON(409, gin.H{"error": "该路径已存在进行中（待处理/执行中）的同类型任务"})
		return
	}
	if err := a.db.Create(task).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.pool.Enqueue()
	c.JSON(201, task)
}

// taskForPath 按任务类型校验路径并构造 Task（单文件/单文件夹维度 = 一条任务）。
// 路径不符合要求时返回 nil 与原因。
func taskForPath(taskType, path string) (*model.Task, string) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "path not found: " + err.Error()
	}
	switch taskType {
	case model.TypeDecompress:
		if info.IsDir() || !archive.IsSupportedArchive(path) {
			return nil, "path is not a supported archive"
		}
	case model.TypeCompress:
		// 允许文件夹或任意文件（压缩包本身除外，压缩压缩包无意义）
		if !info.IsDir() && archive.IsSupportedArchive(path) {
			return nil, "compressing an existing archive is not allowed"
		}
	default:
		return nil, "invalid type: " + taskType
	}
	return &model.Task{Type: taskType, SourcePath: path, Status: model.StatusPending}, ""
}

// insertTasks 用批量插入（分批事务）写入任务并返回回填的 id 列表。
func (a *API) insertTasks(tasks []model.Task) ([]uint, error) {
	if err := a.db.CreateInBatches(&tasks, 500).Error; err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(tasks))
	for i := range tasks {
		ids = append(ids, tasks[i].ID)
	}
	return ids, nil
}

// hasActiveTask 判断同类型、同源路径的任务是否已存在且尚未结束（pending/running）。
// 查询出错时按"无重复"处理，不阻塞任务创建。
func (a *API) hasActiveTask(taskType, path string) bool {
	kept, _ := a.filterActivePaths(taskType, []string{path})
	_, ok := kept[path]
	return !ok
}

// filterActivePaths 过滤掉已存在 pending/running 同源同类型任务的路径，
// 返回保留路径集合与跳过数量。查询出错时按"无重复"处理。
func (a *API) filterActivePaths(taskType string, paths []string) (map[string]struct{}, int) {
	kept := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		kept[p] = struct{}{}
	}
	active := []string{}
	// 分批查询，避免 SQLite 绑定参数数量超限
	for i := 0; i < len(paths); i += 500 {
		end := i + 500
		if end > len(paths) {
			end = len(paths)
		}
		var batch []string
		if err := a.db.Model(&model.Task{}).
			Where("type = ? AND status IN ? AND source_path IN ?", taskType,
				[]string{model.StatusPending, model.StatusRunning}, paths[i:end]).
			Pluck("source_path", &batch).Error; err != nil {
			return kept, 0
		}
		active = append(active, batch...)
	}
	skipped := 0
	for _, p := range active {
		if _, ok := kept[p]; ok {
			delete(kept, p)
			skipped++
		}
	}
	return kept, skipped
}

type createTasksBatchRequest struct {
	Type  string   `json:"type" binding:"required"`
	Paths []string `json:"paths" binding:"required"`
}

// CreateTasksBatch 为勾选的多条路径一次性批量创建任务（每个路径一条独立任务）。
// 不满足类型条件的路径会被跳过并计入 skipped。
func (a *API) CreateTasksBatch(c *gin.Context) {
	var req createTasksBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.Type != model.TypeDecompress && req.Type != model.TypeCompress {
		c.JSON(400, gin.H{"error": "invalid type: " + req.Type})
		return
	}
	// 勾选即创建：每个选中的路径对应一条任务，文件夹整体压缩为一个 .zip，
	// 不展开到其子项（不穿透）。不勾选时的批量压缩才由配置的穿透开关决定。
	paths := req.Paths
	seen := make(map[string]struct{}, len(paths))
	tasks := make([]model.Task, 0, len(paths))
	skipped := 0
	for _, p := range paths {
		if p == "" {
			skipped++
			continue
		}
		if _, dup := seen[p]; dup {
			skipped++
			continue
		}
		seen[p] = struct{}{}
		t, _ := taskForPath(req.Type, p)
		if t == nil {
			skipped++
			continue
		}
		tasks = append(tasks, *t)
	}
	if len(tasks) == 0 {
		msg := "所选内容中没有可创建的压缩任务（压缩包不支持再次压缩）"
		if req.Type == model.TypeDecompress {
			msg = "所选内容中没有可解压的压缩包"
		}
		c.JSON(400, gin.H{"error": msg})
		return
	}
	// 过滤已有进行中任务的路径，避免同源任务并发执行互相冲突
	paths = make([]string, 0, len(tasks))
	for _, t := range tasks {
		paths = append(paths, t.SourcePath)
	}
	kept, dupSkipped := a.filterActivePaths(req.Type, paths)
	if len(kept) == 0 {
		c.JSON(409, gin.H{"error": "所选内容均已存在进行中的任务"})
		return
	}
	filtered := make([]model.Task, 0, len(kept))
	for _, t := range tasks {
		if _, ok := kept[t.SourcePath]; ok {
			filtered = append(filtered, t)
		}
	}
	tasks = filtered
	skipped += dupSkipped
	ids, err := a.insertTasks(tasks)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.pool.Enqueue()
	c.JSON(201, gin.H{"created": len(ids), "ids": ids, "skipped": skipped})
}

type bulkRequest struct {
	Path      string `json:"path" binding:"required"`
	Recursive *bool  `json:"recursive"`
}

// BulkDecompress 扫描文件夹下的所有压缩包，每个压缩包创建一个独立任务。
func (a *API) BulkDecompress(c *gin.Context) {
	var req bulkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	info, err := os.Stat(req.Path)
	if err != nil || !info.IsDir() {
		c.JSON(400, gin.H{"error": "path is not a directory"})
		return
	}
	recursive := true
	if req.Recursive != nil {
		recursive = *req.Recursive
	}
	var archives []string
	err = filepath.WalkDir(req.Path, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			// 跳过任务执行期的临时目录，避免扫到运行中任务的产物
			if strings.HasPrefix(d.Name(), worker.TempDirPrefix) {
				return filepath.SkipDir
			}
			if !recursive && p != req.Path {
				return filepath.SkipDir
			}
			return nil
		}
		if archive.IsSupportedArchive(p) {
			archives = append(archives, p)
		}
		return nil
	})
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	sort.Slice(archives, func(i, j int) bool {
		return strings.ToLower(archives[i]) < strings.ToLower(archives[j])
	})
	if len(archives) == 0 {
		c.JSON(400, gin.H{"error": "所选目录下没有可解压的压缩包"})
		return
	}
	// 过滤已有进行中任务的压缩包，避免同源任务并发执行互相冲突
	kept, dupSkipped := a.filterActivePaths(model.TypeDecompress, archives)
	tasks := make([]model.Task, 0, len(kept))
	for _, arc := range archives {
		if _, ok := kept[arc]; ok {
			tasks = append(tasks, model.Task{Type: model.TypeDecompress, SourcePath: arc, Status: model.StatusPending})
		}
	}
	if len(tasks) == 0 {
		c.JSON(409, gin.H{"error": "所选目录下的压缩包均已存在进行中的任务"})
		return
	}
	ids, err := a.insertTasks(tasks)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.pool.Enqueue()
	c.JSON(201, gin.H{"created": len(ids), "ids": ids, "skipped": dupSkipped})
}

// BulkCompress 扫描文件夹下的非压缩包条目，每个创建独立压缩任务。
// recursive=false（默认）：当前目录下所有项目（文件夹+文件）都压缩。
// recursive=true：穿透子文件夹，只压缩文件，不压缩文件夹本身。
func (a *API) BulkCompress(c *gin.Context) {
	var req bulkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	info, err := os.Stat(req.Path)
	if err != nil || !info.IsDir() {
		c.JSON(400, gin.H{"error": "path is not a directory"})
		return
	}
	recursive := false
	if req.Recursive != nil {
		recursive = *req.Recursive
	}
	var targets []string
	err = filepath.WalkDir(req.Path, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == req.Path {
			return nil
		}
		// 跳过任务执行期的临时目录，避免扫到运行中任务的产物
		if d.IsDir() && strings.HasPrefix(d.Name(), worker.TempDirPrefix) {
			return filepath.SkipDir
		}
		// 压缩包本身不压缩
		if !d.IsDir() && archive.IsSupportedArchive(p) {
			return nil
		}
		if d.IsDir() {
			if recursive {
				return nil // 递归：继续进入子目录，文件夹本身不压缩
			}
			// 非递归：文件夹本身作为压缩对象，不进入子目录
			targets = append(targets, p)
			return filepath.SkipDir
		}
		// 空文件不压缩
		if fi, err := d.Info(); err != nil || fi.Size() == 0 {
			return nil
		}
		targets = append(targets, p)
		return nil
	})
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	sort.Slice(targets, func(i, j int) bool {
		return strings.ToLower(targets[i]) < strings.ToLower(targets[j])
	})
	if len(targets) == 0 {
		c.JSON(400, gin.H{"error": "所选目录下没有可压缩的条目"})
		return
	}
	// 过滤已有进行中任务的路径，避免同源任务并发执行互相冲突
	kept, dupSkipped := a.filterActivePaths(model.TypeCompress, targets)
	tasks := make([]model.Task, 0, len(kept))
	for _, t := range targets {
		if _, ok := kept[t]; ok {
			tasks = append(tasks, model.Task{Type: model.TypeCompress, SourcePath: t, Status: model.StatusPending})
		}
	}
	if len(tasks) == 0 {
		c.JSON(409, gin.H{"error": "所选目录下的条目均已存在进行中的任务"})
		return
	}
	ids, err := a.insertTasks(tasks)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.pool.Enqueue()
	c.JSON(201, gin.H{"created": len(ids), "ids": ids, "skipped": dupSkipped})
}

// ListTasks 分页 + 筛选任务列表。
func (a *API) ListTasks(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := a.db.Model(&model.Task{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("type"); v != "" {
		q = q.Where("type = ?", v)
	}
	if v := c.Query("source_path"); v != "" {
		q = q.Where("source_path LIKE ?", "%"+v+"%")
	}
	if v := c.Query("completed_after"); v != "" {
		if t, err := parseTime(v); err == nil {
			q = q.Where("completed_at >= ?", t)
		}
	}
	if v := c.Query("completed_before"); v != "" {
		if t, err := parseTime(v); err == nil {
			if isDateOnly(v) {
				// 纯日期作为截止条件时，包含该日 0:00 至 23:59:59 全天。
				q = q.Where("completed_at < ?", t.AddDate(0, 0, 1))
			} else {
				q = q.Where("completed_at <= ?", t)
			}
		}
	}
	var total int64
	q.Count(&total)
	var items []model.Task
	if err := q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetTask 返回单个任务详情（含进度字段）。
func (a *API) GetTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid id"})
		return
	}
	var task model.Task
	if err := a.db.First(&task, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "task not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, task)
}

// RetryTask 手动重试一个失败任务：清理临时文件、校验可重做后补一条 pending 任务。
// 原记录保留为 failed 历史，其 error 指向新任务 id。
func (a *API) RetryTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid id"})
		return
	}
	var task model.Task
	if err := a.db.First(&task, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "task not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if task.Status != model.StatusFailed {
		c.JSON(400, gin.H{"error": "只有失败的任务可以重试"})
		return
	}
	nt, rerr := worker.Requeue(a.db, &task)
	if rerr != nil {
		c.JSON(400, gin.H{"error": rerr.Error()})
		return
	}
	// 原记录保留历史，仅补充指向新任务的说明（不改动完成时间）
	a.db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"error":     fmt.Sprintf("已手动重试，新任务 #%d", nt.ID),
		"temp_path": "",
	})
	a.pool.Enqueue()
	c.JSON(201, gin.H{"id": nt.ID, "requeue_count": nt.RequeueCount})
}

// WakeTasks 手动唤醒调度器：立即重新扫描 pending 任务并补满空闲并发槽。
// 正常路径（建任务 / 任务结束 / 调整并发 / 重试）都会自动唤醒，
// 此接口用于异常情况下由用户在配置页兜底唤醒。
func (a *API) WakeTasks(c *gin.Context) {
	var pending, running int64
	a.db.Model(&model.Task{}).Where("status = ?", model.StatusPending).Count(&pending)
	a.db.Model(&model.Task{}).Where("status = ?", model.StatusRunning).Count(&running)
	a.pool.Enqueue()
	c.JSON(200, gin.H{"ok": true, "pending": pending, "running": running})
}

// DeleteTask 删除任务记录（仅允许已结束任务）。
func (a *API) DeleteTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid id"})
		return
	}
	var task model.Task
	if err := a.db.First(&task, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "task not found"})
			return
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if task.Status != model.StatusSucceeded && task.Status != model.StatusFailed {
		c.JSON(400, gin.H{"error": "only finished tasks (succeeded/failed) can be deleted"})
		return
	}
	a.db.Delete(&task)
	c.JSON(200, gin.H{"ok": true})
}

// parseTime 解析任务筛选时间参数。
// 支持带时区的 RFC3339，以及服务器本地时区下常见的日期/日期时间格式。
// 统一转到 time.Local，保证与入库的 completed_at（time.Now 本地时区）可一致比较。
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(time.Local), nil
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

// isDateOnly 判断参数是否为纯日期 YYYY-MM-DD（不带时间）。
func isDateOnly(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// FileStatus 返回指定路径的目录最后修改时间（unix 秒）。
// 前端以固定周期轮询此接口，用 modified 与本地基线对比，决定是否需要刷新文件列表。
// 目录内容变化（增删/重命名直接子项）即更新 mtime，正是"列表需要刷新"的充要条件。
func (a *API) FileStatus(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		c.JSON(400, gin.H{"error": "path is required"})
		return
	}
	var modified int64
	if info, err := os.Stat(path); err == nil {
		modified = info.ModTime().Unix()
	}
	c.JSON(200, gin.H{"modified": modified})
}
