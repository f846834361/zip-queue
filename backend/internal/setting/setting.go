// Package setting 读写运行期可在配置页修改的项（持久化在 settings 表，
// 缺失时使用调用方提供的默认值）。被 api / worker / cmd 共用，避免重复查询逻辑。
package setting

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zip-queue/internal/model"
)

// 运行期配置项的存储 key。
const (
	// KeyPenetrateSubfolders 批量操作"穿透文件夹"开关。
	KeyPenetrateSubfolders = "penetrate_subfolders"
	// KeyMaxConcurrentTasks 同时执行任务数。
	KeyMaxConcurrentTasks = "max_concurrent_tasks"
	// KeyCompressionLevel 压缩效率（fastest / fast / normal / slow）。
	KeyCompressionLevel = "compression_level"
	// KeyStripFolder 压缩时"去掉顶层文件夹"开关：true 时 zip 内不保留被选中的顶层
	// 目录这一层（条目退化为 a.txt / sub/b.txt，即修复 commit 2f948182a 之前的逻辑）；
	// false（默认）保留顶层目录（MyFolder/a.txt），与 PC 右键压缩一致（修复后逻辑）。
	KeyStripFolder = "strip_folder"
	// KeyAddFolderMode 解压"智能添加文件夹"模式（取值见 AddFolder* 常量）。
	KeyAddFolderMode = "add_folder_mode"
)

// 解压"智能添加文件夹"模式的可选值（与配置页下拉框、后端校验共用）。
// 当前默认 AddFolderOne：多个文件或单个文件都包一层以 zip 名命名的父文件夹，
// 同时避免"文件夹套文件夹"（zip 内本就是单个文件夹时不重复包）。
const (
	// AddFolderOne 「1个」：多个文件或单个文件都加父文件夹；单个文件夹不加（避免嵌套）。
	AddFolderOne = "one"
	// AddFolderMultiple 「多个」：仅当解压出多个顶层条目才加父文件夹；单个文件/文件夹不加。
	AddFolderMultiple = "multiple"
	// AddFolderNone 「无」：始终不自动加父文件夹。
	AddFolderNone = "none"
)

// ValidAddFolderMode 判断字符串是否为合法的 add_folder_mode 取值。
func ValidAddFolderMode(v string) bool {
	return v == AddFolderOne || v == AddFolderMultiple || v == AddFolderNone
}

// 同时执行任务数的可选范围：页面下拉框与后端校验共用。
const (
	MinConcurrency = 1
	MaxConcurrency = 4
)

// Get 读取指定 key 的配置值，不存在或读取失败时返回空串。
// 用 Find 而非 First：未配置的 key 是正常情况，避免 GORM 打印 record not found 错误日志。
func Get(db *gorm.DB, key string) string {
	var rows []model.Setting
	if err := db.Where("key = ?", key).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].Value
}

// Set 写入配置，key 已存在时覆盖 value 与 updated_at。
func Set(db *gorm.DB, key, value string) error {
	s := model.Setting{Key: key, Value: value, UpdatedAt: time.Now()}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&s).Error
}

// ClampConcurrency 把并发数夹紧到 [MinConcurrency, MaxConcurrency]：
// 未设置或非法（<=0）回落最小值，超过上限取最大值。
func ClampConcurrency(n int) int {
	if n < MinConcurrency {
		return MinConcurrency
	}
	if n > MaxConcurrency {
		return MaxConcurrency
	}
	return n
}
