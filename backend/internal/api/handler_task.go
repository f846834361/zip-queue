package api

import (
	"errors"
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
	seen := make(map[string]struct{}, len(req.Paths))
	tasks := make([]model.Task, 0, len(req.Paths))
	skipped := 0
	for _, p := range req.Paths {
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
	tasks := make([]model.Task, 0, len(archives))
	for _, arc := range archives {
		tasks = append(tasks, model.Task{Type: model.TypeDecompress, SourcePath: arc, Status: model.StatusPending})
	}
	ids, err := a.insertTasks(tasks)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.pool.Enqueue()
	c.JSON(201, gin.H{"created": len(ids), "ids": ids})
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
		// 压缩包本身不压缩
		if !d.IsDir() && archive.IsSupportedArchive(p) {
			return nil
		}
		if !recursive {
			// 非递归：当前目录下所有非压缩包项目（文件夹+文件）都压缩
			if !d.IsDir() {
				// 空文件不压缩
				if fi, err := d.Info(); err != nil || fi.Size() == 0 {
					return nil
				}
			}
			targets = append(targets, p)
			return filepath.SkipDir // 不进入子目录
		}
		// 递归：只压缩文件，不压缩文件夹本身
		if d.IsDir() {
			return nil // 继续进入子目录
		}
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
	tasks := make([]model.Task, 0, len(targets))
	for _, t := range targets {
		tasks = append(tasks, model.Task{Type: model.TypeCompress, SourcePath: t, Status: model.StatusPending})
	}
	ids, err := a.insertTasks(tasks)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	a.pool.Enqueue()
	c.JSON(201, gin.H{"created": len(ids), "ids": ids})
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
			q = q.Where("completed_at <= ?", t)
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

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}
