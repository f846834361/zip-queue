package worker

import (
	"fmt"
	"os"
	"time"

	"gorm.io/gorm"

	"zip-queue/internal/model"
)

// MaxRequeue 单个任务因服务中断被自动重新排队的上限次数。
// 达到上限后不再自动补任务，避免异常任务在反复重启中无限重排。
const MaxRequeue = 3

// FinishInterrupted 处理被中断的任务（服务停止 / 重启），是「中断收尾」的唯一入口：
//  1. 删除临时文件（临时目录或临时压缩包）；
//  2. 依据磁盘上源文件与目标的真实存在情况判定该任务的最终状态；
//  3. 若可以安全重做（源在、目标尚未生成）且未超过重排上限，补一条 pending 任务，
//     created_at = 当前时间（即停服/重启时刻），排到队尾，重启后由调度器继续执行。
//
// 旧任务记录保留为历史（failed / succeeded）以便追溯，不复用其 id。
// 返回补建的新任务 id 与是否补建成功。
func FinishInterrupted(db *gorm.DB, t *model.Task) (uint, bool) {
	// 1. 清理临时文件
	if t.TempPath != "" {
		_ = os.RemoveAll(t.TempPath)
	}

	srcExists := pathExists(t.SourcePath)
	targetExists := t.TargetPath != "" && pathExists(t.TargetPath)

	switch {
	// 产物已就位且源文件已删除：其实已经完成，只是没来得及落库成功状态
	case !srcExists && targetExists:
		finishInterrupted(db, t.ID, model.StatusSucceeded, "", true)
		return 0, false

	// 源在、目标未生成：删掉临时文件后可安全重做
	case srcExists && !targetExists:
		nt, err := Requeue(db, t)
		if err != nil {
			finishInterrupted(db, t.ID, model.StatusFailed,
				"任务被服务中断，自动重新排队失败："+err.Error(), false)
			return 0, false
		}
		finishInterrupted(db, t.ID, model.StatusFailed,
			fmt.Sprintf("任务被服务中断，已重新排队为新任务 #%d（第 %d 次重排）", nt.ID, nt.RequeueCount), false)
		return nt.ID, true

	// 源和目标都在：重做会撞「目标已存在」，交给人工确认
	case srcExists && targetExists:
		finishInterrupted(db, t.ID, model.StatusFailed,
			"任务被服务中断，但目标已生成："+t.TargetPath+"，请人工确认后再处理", false)
		return 0, false

	// 源和目标都不在：无法继续
	default:
		finishInterrupted(db, t.ID, model.StatusFailed,
			"任务被服务中断，且源文件已不存在，无法继续", false)
		return 0, false
	}
}

// Requeue 判断任务能否安全重做，可以则补一条 pending 任务：
// 清理残留临时文件 → 校验次数上限与源/目标状态 → 插入新任务（RequeueCount+1，created_at=now）。
// 不可重做时返回具体原因（供前端提示）。调用方负责旧任务记录的状态落库。
func Requeue(db *gorm.DB, t *model.Task) (*model.Task, error) {
	if t.TempPath != "" {
		_ = os.RemoveAll(t.TempPath)
	}
	if t.RequeueCount >= MaxRequeue {
		return nil, fmt.Errorf("重排/重试次数已达上限（%d 次），请手动处理", MaxRequeue)
	}
	if !pathExists(t.SourcePath) {
		return nil, fmt.Errorf("源文件已不存在，无法继续执行：%s", t.SourcePath)
	}
	if t.TargetPath != "" && pathExists(t.TargetPath) {
		return nil, fmt.Errorf("目标已生成，重试会冲突，请先处理：%s", t.TargetPath)
	}
	nt := model.Task{
		Type:         t.Type,
		SourcePath:   t.SourcePath,
		Status:       model.StatusPending,
		RequeueCount: t.RequeueCount + 1,
		CreatedAt:    time.Now(),
	}
	if err := db.Create(&nt).Error; err != nil {
		return nil, err
	}
	return &nt, nil
}

// finishInterrupted 写入中断任务的最终状态（临时路径一律清空）。
func finishInterrupted(db *gorm.DB, id uint, status, errMsg string, succeeded bool) {
	updates := map[string]interface{}{
		"status":       status,
		"completed_at": time.Now(),
		"temp_path":    "",
	}
	if succeeded {
		updates["progress_percent"] = 100
		updates["error"] = ""
	} else {
		updates["error"] = truncate(errMsg, 2048)
	}
	db.Model(&model.Task{}).Where("id = ?", id).Updates(updates)
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}
