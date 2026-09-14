package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"zip-queue/internal/setting"
)

// Health 简单存活检查。
func (a *API) Health(c *gin.Context) {
	c.JSON(200, gin.H{"ok": true})
}

// stripFolder 读取"去掉顶层文件夹"开关，默认 false。
func (a *API) stripFolder() bool {
	v, err := strconv.ParseBool(setting.Get(a.db, setting.KeyStripFolder))
	return err == nil && v
}

// addFolderMode 读取"智能添加文件夹"模式，未设置回落默认 1个（one）。
func (a *API) addFolderMode() string {
	v := setting.Get(a.db, setting.KeyAddFolderMode)
	if !setting.ValidAddFolderMode(v) {
		return setting.AddFolderNone
	}
	return v
}

// configResponse 返回前端需要展示/编辑的运行参数。
// max_concurrent_tasks 与 compression_level 取页面保存后的生效值（DB 覆盖优先）。
func (a *API) configResponse() gin.H {
	return gin.H{
		"max_concurrent_tasks": a.maxConcurrentTasks(),
		"default_browse_path":  a.cfg.Browse.DefaultPath,
		"penetrate_subfolders": a.penetrateSubfolders(),
		"compression_level":    a.compressionLevel(),
		"strip_folder":         a.stripFolder(),
		"add_folder_mode":      a.addFolderMode(),
	}
}

// Config 返回前端需要展示的运行参数。
func (a *API) Config(c *gin.Context) {
	c.JSON(200, a.configResponse())
}
