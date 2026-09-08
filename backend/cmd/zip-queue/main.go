package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zip-queue/internal/archive"
	"zip-queue/internal/config"
	"zip-queue/internal/db"
	"zip-queue/internal/server"
	"zip-queue/internal/worker"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	log.Printf("zip-queue starting: port=%d db=%s concurrency=%d",
		cfg.Server.Port, cfg.DB.Path, cfg.Worker.MaxConcurrentTasks)

	gdb, err := db.Open(cfg.DB.Path, cfg.Log.Level)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
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

	runner := worker.NewRunner(gdb, archive.Limits{MaxTotalBytes: cfg.Worker.MaxExtractTotalBytes})
	pool := worker.NewPool(gdb, runner, cfg.Worker.MaxConcurrentTasks)

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
