package worker

import (
	"gorm.io/gorm"

	"zip-queue/internal/model"
)

// RecoverOnStartup 在进程启动时调用：处理上一进程被强制结束（SIGKILL / 崩溃 / 容器超时）
// 时残留的 running 任务——清理临时文件、判定最终状态，可安全重做的补一条 pending 任务。
// 返回处理数量与其中重新排队的数量。
//
// pending 任务不做任何改动，由调度器按 created_at 顺序继续执行。
// 单实例部署下，启动时残留的 running 一定是孤儿任务，可安全重排。
func RecoverOnStartup(db *gorm.DB) (int, int, error) {
	var tasks []model.Task
	if err := db.Where("status = ?", model.StatusRunning).Find(&tasks).Error; err != nil {
		return 0, 0, err
	}
	if len(tasks) == 0 {
		return 0, 0, nil
	}
	requeued := 0
	for i := range tasks {
		if _, ok := FinishInterrupted(db, &tasks[i]); ok {
			requeued++
		}
	}
	return len(tasks), requeued, nil
}
