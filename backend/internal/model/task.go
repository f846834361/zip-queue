package model

import "time"

// 任务类型
const (
	TypeDecompress = "decompress"
	TypeCompress   = "compress"
	// TypeDedup 为"查重"任务：只读扫描所选路径，找出内容重复的文件/文件夹，不改动任何文件。
	TypeDedup = "dedup"
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
	ID              uint   `gorm:"primaryKey" json:"id"`
	Type            string `gorm:"size:16;index" json:"type"`
	SourcePath      string `gorm:"size:1024;index" json:"source_path"`
	TargetPath      string `gorm:"size:1024" json:"target_path"`
	TempPath        string `gorm:"size:1024" json:"temp_path"`
	Status          string `gorm:"size:16;index" json:"status"`
	ProgressPercent int    `json:"progress_percent"`
	ProcessedBytes  int64  `json:"processed_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	Error           string `gorm:"size:2048" json:"error"`
	// CompressionLevel 记录该压缩任务执行时实际使用的压缩效率档位
	// （fastest/fast/normal/slow）；解压任务不涉及压缩效率变更，此项为空。
	CompressionLevel string `gorm:"size:16" json:"compression_level"`
	// RequeueCount 记录该任务因「服务中断」被自动重新排队的次数（用于限制无限重排）。
	RequeueCount int `json:"requeue_count"`
	// Sources 记录查重任务的输入路径集合（JSON 字符串数组）；解压/压缩任务为空。
	Sources string `gorm:"type:text" json:"sources"`
	// Result 记录查重任务的结论（JSON，结构见 DedupResult）；其他任务类型为空。
	Result      string     `gorm:"type:text" json:"result"`
	CreatedAt   time.Time  `gorm:"index" json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `gorm:"index" json:"completed_at"`
}

func (Task) TableName() string { return "tasks" }

// DedupResult 是查重任务（TypeDedup）的结论，以 JSON 存入 Task.Result。
type DedupResult struct {
	// DuplicateFiles 为内容完全相同的重复文件分组。
	DuplicateFiles []DedupGroup `json:"duplicate_files"`
	// DuplicateFolders 为"所含文件哈希集合相同（忽略目录层级）"的重复文件夹分组。
	DuplicateFolders []DedupGroup `json:"duplicate_folders"`
	// WastedBytes 为删除重复副本后可节省的空间（按重复文件组计算）。
	WastedBytes  int64 `json:"wasted_bytes"`
	TotalFiles   int   `json:"total_files"`
	TotalFolders int   `json:"total_folders"`
	// CheckedBytes 为本次实际从磁盘读取的字节数（采样 + 全量哈希）。
	CheckedBytes int64 `json:"checked_bytes"`
}

// DedupGroup 一组内容相同的条目（文件或文件夹）。
// 同一组的文件几乎总在同一个深层目录下，完整路径逐条存储会让同一段超长前缀重复 N 遍，
// 因此这里只存一次公共前缀，其余存前缀之后的相对部分（展示时拼接即可）。
type DedupGroup struct {
	// Size 为单个条目的大小（文件夹组为其包含文件的总大小）。
	Size int64 `json:"size"`
	// Hash 为文件组的内容哈希；文件夹组为空（其签名由所含文件哈希集合构成）。
	Hash string `json:"hash,omitempty"`
	// Prefix 为该组全部条目的公共目录前缀（以分隔符结尾）；无公共目录时为空串。
	Prefix string `json:"prefix"`
	// Buckets 为在 Prefix 之下按子目录进一步拆分的条目桶：
	// 一组重复文件常分散在若干深层子目录里，逐条存相对路径会让长目录反复出现，
	// 因此同目录的条目合并进同一个桶，桶内只留文件名。
	Buckets []DedupBucket `json:"buckets"`
}

// DedupBucket 是同一子目录下的一组互为副本的条目。
type DedupBucket struct {
	// Prefix 为相对所属分组 Prefix 之后的子目录（以分隔符结尾）；
	// 条目就位于分组根目录下时为空串。
	Prefix string `json:"prefix"`
	// Names 为该子目录下的条目名（路径末段），按名称排序。
	Names []string `json:"names"`
}
