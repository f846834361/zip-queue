package api

import "github.com/gin-gonic/gin"

// Health 简单存活检查。
func (a *API) Health(c *gin.Context) {
	c.JSON(200, gin.H{"ok": true})
}

// configResponse 返回前端需要展示/编辑的运行参数。
func (a *API) configResponse() gin.H {
	return gin.H{
		"max_concurrent_tasks": a.cfg.Worker.MaxConcurrentTasks,
		"default_browse_path":  a.cfg.Browse.DefaultPath,
		"penetrate_subfolders": a.penetrateSubfolders(),
	}
}

// Config 返回前端需要展示的运行参数。
func (a *API) Config(c *gin.Context) {
	c.JSON(200, a.configResponse())
}
