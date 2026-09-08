package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

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

	runner := worker.NewRunner(gdb)
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
	pool.Wait()
	log.Println("zip-queue stopped")
}
