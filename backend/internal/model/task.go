package model

import "time"

// 任务类型
const (
	TypeDecompress = "decompress"
	TypeCompress   = "compress"
)

// 任务状态
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Task 对应一个文件的处理任务（解压或压缩）。
type Task struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Type             string     `gorm:"size:16;index" json:"type"`
	SourcePath       string     `gorm:"size:1024;index" json:"source_path"`
	TargetPath       string     `gorm:"size:1024" json:"target_path"`
	TempPath         string     `gorm:"size:1024" json:"temp_path"`
	Status           string     `gorm:"size:16;index" json:"status"`
	ProgressPercent  int        `json:"progress_percent"`
	ProcessedBytes   int64      `json:"processed_bytes"`
	TotalBytes       int64      `json:"total_bytes"`
	Error            string     `gorm:"size:2048" json:"error"`
	// CompressionLevel 记录该压缩任务执行时实际使用的压缩效率档位
	// （fastest/fast/normal/slow）；解压任务不涉及压缩效率变更，此项为空。
	CompressionLevel string `gorm:"size:16" json:"compression_level"`
	// RequeueCount 记录该任务因「服务中断」被自动重新排队的次数（用于限制无限重排）。
	RequeueCount     int        `json:"requeue_count"`
	CreatedAt        time.Time  `gorm:"index" json:"created_at"`
	StartedAt        *time.Time `json:"started_at"`
	CompletedAt      *time.Time `gorm:"index" json:"completed_at"`
}

func (Task) TableName() string { return "tasks" }
