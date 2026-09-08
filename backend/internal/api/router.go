package api

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"zip-queue/internal/config"
	"zip-queue/internal/worker"
)

// API 持有所有 HTTP 处理器共享的依赖。
type API struct {
	db   *gorm.DB
	pool *worker.Pool
	cfg  *config.Config
}

// Register 在给定路由组上注册全部 /api 路由。
func Register(r *gin.RouterGroup, gdb *gorm.DB, pool *worker.Pool, cfg *config.Config) {
	a := &API{db: gdb, pool: pool, cfg: cfg}
	r.GET("/health", a.Health)
	r.GET("/config", a.Config)
	r.PUT("/config", a.UpdateConfig)
	r.GET("/fs/list", a.ListDir)
	r.POST("/tasks", a.CreateTask)
	r.POST("/tasks/batch", a.CreateTasksBatch)
	r.POST("/tasks/bulk-decompress-folder", a.BulkDecompress)
	r.POST("/tasks/bulk-compress-folder", a.BulkCompress)
	r.GET("/tasks", a.ListTasks)
	r.GET("/tasks/:id", a.GetTask)
	r.DELETE("/tasks/:id", a.DeleteTask)

	r.GET("/passwords", a.ListPasswords)
	r.POST("/passwords", a.CreatePassword)
	r.PATCH("/passwords/:id", a.UpdatePassword)
	r.PATCH("/passwords/:id/enabled", a.SetPasswordEnabled)
	r.DELETE("/passwords/:id", a.DeletePassword)
	r.POST("/passwords/reorder", a.ReorderPasswords)
}
