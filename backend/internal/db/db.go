package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"zip-queue/internal/model"
)

// Open 打开/创建 SQLite 文件并执行自动迁移。
func Open(dbPath string, logLevel string) (*gorm.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	gormCfg := &gorm.Config{
		Logger: logger.Default.LogMode(parseLogLevel(logLevel)),
	}
	gdb, err := gorm.Open(sqlite.Open(dbPath), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := gdb.AutoMigrate(&model.Task{}, &model.Password{}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return gdb, nil
}

func parseLogLevel(s string) logger.LogLevel {
	switch strings.ToLower(s) {
	case "silent":
		return logger.Silent
	case "error":
		return logger.Error
	case "warn":
		return logger.Warn
	case "info":
		return logger.Info
	default:
		return logger.Warn
	}
}
