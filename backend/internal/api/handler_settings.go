package api

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"zip-queue/internal/archive"
	"zip-queue/internal/setting"
)

// penetrateSubfolders 读取"穿透文件夹"开关，默认 false。
func (a *API) penetrateSubfolders() bool {
	v, err := strconv.ParseBool(setting.Get(a.db, setting.KeyPenetrateSubfolders))
	return err == nil && v
}

// maxConcurrentTasks 返回生效的并发数：DB 覆盖值优先，未设置时回落 yaml/env 默认值。
func (a *API) maxConcurrentTasks() int {
	if v := setting.Get(a.db, setting.KeyMaxConcurrentTasks); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return setting.ClampConcurrency(n)
		}
	}
	return setting.ClampConcurrency(a.cfg.Worker.MaxConcurrentTasks)
}

// compressionLevel 返回生效的压缩效率，未设置时回落默认档位。
func (a *API) compressionLevel() archive.Level {
	return archive.ParseLevel(setting.Get(a.db, setting.KeyCompressionLevel))
}

type updateConfigRequest struct {
	PenetrateSubfolders *bool   `json:"penetrate_subfolders"`
	MaxConcurrentTasks  *int    `json:"max_concurrent_tasks"`
	CompressionLevel    *string `json:"compression_level"`
	StripFolder         *bool   `json:"strip_folder"`
	AddFolderMode       *string `json:"add_folder_mode"`
	SkipCompressed      *bool   `json:"skip_compressed"`
}

// UpdateConfig 更新可在配置页修改的运行期配置项。
func (a *API) UpdateConfig(c *gin.Context) {
	var req updateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	// 先校验全部入参再落库，避免非法值造成部分写入
	if req.MaxConcurrentTasks != nil {
		n := *req.MaxConcurrentTasks
		if n < setting.MinConcurrency || n > setting.MaxConcurrency {
			c.JSON(400, gin.H{"error": fmt.Sprintf("同时执行任务数必须在 %d-%d 之间", setting.MinConcurrency, setting.MaxConcurrency)})
			return
		}
	}
	if req.CompressionLevel != nil && !archive.Level(*req.CompressionLevel).Valid() {
		c.JSON(400, gin.H{"error": "压缩效率取值非法：" + *req.CompressionLevel})
		return
	}
	if req.AddFolderMode != nil && !setting.ValidAddFolderMode(*req.AddFolderMode) {
		c.JSON(400, gin.H{"error": "智能添加文件夹取值非法：" + *req.AddFolderMode})
		return
	}

	if req.PenetrateSubfolders != nil {
		if err := setting.Set(a.db, setting.KeyPenetrateSubfolders, strconv.FormatBool(*req.PenetrateSubfolders)); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	if req.MaxConcurrentTasks != nil {
		if err := setting.Set(a.db, setting.KeyMaxConcurrentTasks, strconv.Itoa(*req.MaxConcurrentTasks)); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		// 立即调整 worker 池上限：已派发的任务不受影响，结束后按新上限补派
		a.pool.SetConcurrency(*req.MaxConcurrentTasks)
	}
	if req.CompressionLevel != nil {
		if err := setting.Set(a.db, setting.KeyCompressionLevel, *req.CompressionLevel); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	if req.StripFolder != nil {
		if err := setting.Set(a.db, setting.KeyStripFolder, strconv.FormatBool(*req.StripFolder)); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	if req.AddFolderMode != nil {
		if err := setting.Set(a.db, setting.KeyAddFolderMode, *req.AddFolderMode); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	if req.SkipCompressed != nil {
		if err := setting.Set(a.db, setting.KeySkipCompressed, strconv.FormatBool(*req.SkipCompressed)); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(200, a.configResponse())
}
