package api

import (
	"errors"
	"os"

	"github.com/gin-gonic/gin"

	"zip-queue/internal/fs"
)

// ListDir 列出 path 目录；path 为空时使用配置的默认浏览根。
// 同时返回该目录的最后修改时间（unix 秒），供前端轮询时对比是否需要刷新。
func (a *API) ListDir(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		path = a.cfg.Browse.DefaultPath
	}
	if c.Query("refresh") == "1" {
		fs.InvalidateListCache(path)
	}
	entries, modTime, err := fs.ListCached(path, a.cfg.Browse.CacheTTL)
	if err != nil {
		var nde *fs.NotDirError
		if errors.As(err, &nde) {
			c.JSON(400, gin.H{"error": nde.Error()})
			return
		}
		if os.IsNotExist(err) {
			c.JSON(404, gin.H{"error": "路径不存在：" + path})
			return
		}
		c.JSON(404, gin.H{"error": "路径无法访问：" + path})
		return
	}
	c.JSON(200, gin.H{"path": path, "entries": entries, "modified": modTime.Unix()})
}
