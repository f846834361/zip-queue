package server

import (
	"context"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"zip-queue/internal/api"
	"zip-queue/internal/config"
	"zip-queue/internal/worker"
	zipweb "zip-queue/web"
)

// Run 启动 HTTP 服务；ctx 取消时优雅关闭（5s 上限）。
func Run(ctx context.Context, cfg *config.Config, gdb *gorm.DB, pool *worker.Pool) error {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), recoveryWithLog())
	api.Register(r.Group("/api"), gdb, pool, cfg)
	r.NoRoute(serveSPA)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           r,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// recoveryWithLog 捕获 HTTP handler 中的 panic：把堆栈记录到标准 log 并返回 500，
// 避免单个请求的 panic 拖垮整个进程；与 worker 协程的 recover 共用同一日志出口。
// （替换 gin.Recovery() 是因为后者默认把堆栈写到 os.Stdout，不是应用统一日志。）
func recoveryWithLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %s %s: %v\n%s", c.Request.Method, c.Request.URL.Path, err, debug.Stack())
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

// serveSPA 提供嵌入的前端静态资源；未命中则回退到 index.html（SPA 路由）。
// Vite 产物文件名带内容 hash，可长缓存；index.html 与路由回退必须 no-cache。
func serveSPA(c *gin.Context) {
	p := strings.TrimPrefix(c.Request.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	target := path.Join("dist", p)
	data, err := zipweb.FS.ReadFile(target)
	if err != nil {
		data, err = zipweb.FS.ReadFile(path.Join("dist", "index.html"))
		if err != nil {
			c.String(http.StatusNotFound, "frontend not built")
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, contentType("index.html"), data)
		return
	}
	if p == "index.html" {
		c.Header("Cache-Control", "no-cache")
	} else {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
	c.Data(http.StatusOK, contentType(target), data)
}

func contentType(p string) string {
	if ct := mime.TypeByExtension(path.Ext(p)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
