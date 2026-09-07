package worker

import (
	"os"
	"time"

	"gorm.io/gorm"

	"zip-queue/internal/model"
)

// RecoverOnStartup 在进程启动时调用：将所有处于 running 的任务（容器被强制结束时的残留）
// 标记为 failed 并清理其临时文件。pending 任务保持不变，由调度器继续处理。
func RecoverOnStartup(db *gorm.DB) (int, error) {
	var tasks []model.Task
	if err := db.Where("status = ?", model.StatusRunning).Find(&tasks).Error; err != nil {
		return 0, err
	}
	if len(tasks) == 0 {
		return 0, nil
	}
	now := time.Now()
	for _, t := range tasks {
		if t.TempPath != "" {
			_ = os.RemoveAll(t.TempPath)
		}
		db.Model(&model.Task{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
			"status":       model.StatusFailed,
			"error":        "interrupted by container restart",
			"completed_at": now,
			"temp_path":    "",
		})
	}
	return len(tasks), nil
}
