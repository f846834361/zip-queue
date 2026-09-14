package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"zip-queue/internal/archive"
	"zip-queue/internal/config"
	"zip-queue/internal/db"
	"zip-queue/internal/server"
	"zip-queue/internal/setting"
	"zip-queue/internal/worker"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	setupLogging(cfg)

	gdb, err := db.Open(cfg.DB.Path, cfg.Log.Level)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	// 并发数与压缩效率可在配置页修改并持久化在 settings 表，启动时读回覆盖 yaml/env 默认值，
	// 避免页面显示与 pool 实际并发不一致。
	concurrency := resolveConcurrency(cfg.Worker.MaxConcurrentTasks, gdb)
	compression := archive.ParseLevel(setting.Get(gdb, setting.KeyCompressionLevel))
	log.Printf("zip-queue starting: port=%d db=%s concurrency=%d compression=%s",
		cfg.Server.Port, cfg.DB.Path, concurrency, compression)
	defer func() {
		if sqlDB, err := gdb.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	// 启动恢复：处理上次进程被强制结束时残留的 running 任务
	// （清理临时文件 → 判定最终状态 → 可安全重做的补一条 pending 任务继续排队）。
	recovered, requeued, err := worker.RecoverOnStartup(gdb)
	if err != nil {
		log.Printf("recover on startup: %v", err)
	} else if recovered > 0 {
		log.Printf("recovered %d interrupted tasks (requeued %d, temp cleaned)", recovered, requeued)
	}

	pool := worker.NewPool(gdb, nil, concurrency)
	runner := worker.NewRunner(gdb, archive.Limits{
		MaxTotalBytes: cfg.Worker.MaxExtractTotalBytes,
		MaxRatio:      cfg.Worker.MaxExtractRatio,
	}, pool)
	pool.SetRunner(runner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	// 信号：优雅关闭（SIGTERM/SIGINT）
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutdown signal received, draining workers...")
		cancel()
	}()

	if err := server.Run(ctx, cfg, gdb, pool); err != nil {
		log.Fatalf("server: %v", err)
	}
	// 等待进行中的任务收尾；大文件拷贝无法立即打断，超时则放弃等待，
	// 残留 running 任务由下次启动的 RecoverOnStartup 兜底恢复。
	done := make(chan struct{})
	go func() {
		pool.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		log.Println("warning: workers 未在 30s 内收尾，强制退出")
	}
	log.Println("zip-queue stopped")
}

// setupLogging 在 persist 开启时把标准 log 与 gin 访问日志同时写入日志文件（仍保留 stderr），
// 使容器部署也能把日志落盘持久化；未开启则维持默认的 stderr 输出。
func setupLogging(cfg *config.Config) {
	if !cfg.Log.Persist {
		return
	}
	path := cfg.Log.File
	if path == "" {
		path = "./logs/zip-queue.log"
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("无法创建日志目录 %s，日志仍仅输出到 stderr：%v", dir, err)
			return
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("无法打开日志文件 %s，日志仍仅输出到 stderr：%v", path, err)
		return
	}
	w := io.MultiWriter(os.Stderr, f)
	log.SetOutput(w)
	// gin 访问日志默认走 gin.DefaultWriter，与标准 log 共用同一落盘点以免日志分散。
	gin.DefaultWriter = w
	log.Printf("日志持久化已开启，写入文件：%s", path)
}

// resolveConcurrency 决定启动时的并发数：settings 表中保存的页面配置优先，
// 未保存或值非法时回落 yaml/env 默认值；结果夹紧到合法范围。
func resolveConcurrency(yamlDefault int, gdb *gorm.DB) int {
	if v := setting.Get(gdb, setting.KeyMaxConcurrentTasks); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("忽略非法的 max_concurrent_tasks 配置 %q：%v", v, err)
		} else {
			return setting.ClampConcurrency(n)
		}
	}
	return setting.ClampConcurrency(yamlDefault)
}
