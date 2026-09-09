package api

import "github.com/gin-gonic/gin"

// Health 简单存活检查。
func (a *API) Health(c *gin.Context) {
	c.JSON(200, gin.H{"ok": true})
}

// configResponse 返回前端需要展示/编辑的运行参数。
// max_concurrent_tasks 与 compression_level 取页面保存后的生效值（DB 覆盖优先）。
func (a *API) configResponse() gin.H {
	return gin.H{
		"max_concurrent_tasks": a.maxConcurrentTasks(),
		"default_browse_path":  a.cfg.Browse.DefaultPath,
		"penetrate_subfolders": a.penetrateSubfolders(),
		"compression_level":    a.compressionLevel(),
	}
}

// Config 返回前端需要展示的运行参数。
func (a *API) Config(c *gin.Context) {
	c.JSON(200, a.configResponse())
}
