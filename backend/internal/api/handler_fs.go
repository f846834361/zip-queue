package api

import (
	"errors"

	"github.com/gin-gonic/gin"

	"zip-queue/internal/fs"
)

// ListDir 列出 path 目录；path 为空时使用配置的默认浏览根。
func (a *API) ListDir(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		path = a.cfg.Browse.DefaultPath
	}
	entries, err := fs.List(path)
	if err != nil {
		var nde *fs.NotDirError
		if errors.As(err, &nde) {
			c.JSON(400, gin.H{"error": nde.Error()})
			return
		}
		c.JSON(404, gin.H{"error": "path not accessible: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"path": path, "entries": entries})
}
