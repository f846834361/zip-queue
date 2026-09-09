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
	gdb, err := gorm.Open(sqlite.Open(sqliteDSN(dbPath)), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := gdb.AutoMigrate(&model.Task{}, &model.Password{}, &model.Setting{}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// 移除历史遗留的"条目进度"列（processed_entries / total_entries / current_entry）。
	// GORM AutoMigrate 不会自动删列，这里显式清理。
	m := gdb.Migrator()
	for _, col := range []string{"processed_entries", "total_entries", "current_entry"} {
		if m.HasColumn(&model.Task{}, col) {
			_ = m.DropColumn(&model.Task{}, col)
		}
	}
	return gdb, nil
}

// sqliteDSN 为 SQLite 路径构造 DSN：启用 WAL（允许读写并发）与 busy_timeout
// （worker 进度回写与 API 并发写时等待锁而非直接报 SQLITE_BUSY）。
func sqliteDSN(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return filepath.ToSlash(path) + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
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
