package worker

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"gorm.io/gorm"

	"zip-queue/internal/model"
	"zip-queue/internal/setting"
)

// Pool 管理并发 worker：从 DB 取 pending 任务、原子认领、分发给 runner。
type Pool struct {
	db     *gorm.DB
	runner *Runner
	wake   chan struct{}  // 非阻塞唤醒信号
	wg     sync.WaitGroup // 跟踪进行中的 worker

	// 并发控制：max 为上限（可在运行期调整），running 为已派发且未结束的任务数。
	// 用计数器而非固定容量 channel，使并发上限可以被配置页热更新。
	mu      sync.Mutex
	max     int
	running int
}

func NewPool(db *gorm.DB, runner *Runner, concurrency int) *Pool {
	return &Pool{
		db:     db,
		runner: runner,
		wake:   make(chan struct{}, 1),
		max:    setting.ClampConcurrency(concurrency),
	}
}

// SetConcurrency 调整并发上限（夹紧到合法范围），返回实际生效值。
// 调大时唤醒调度器立即补派任务；调小时不打断已派发任务，其结束后按新上限派发。
func (p *Pool) SetConcurrency(n int) int {
	p.mu.Lock()
	p.max = setting.ClampConcurrency(n)
	next := p.max
	p.mu.Unlock()
	p.Enqueue()
	return next
}

// Concurrency 返回当前并发上限。
func (p *Pool) Concurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max
}

// Start 启动调度循环。返回后立即开始处理 pending 任务。
func (p *Pool) Start(ctx context.Context) {
	go p.loop(ctx)
}

// Enqueue 非阻塞通知调度器有新任务可派发。
func (p *Pool) Enqueue() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Wait 阻塞直到所有进行中的 worker 结束（用于优雅关闭）。
func (p *Pool) Wait() {
	p.wg.Wait()
}

// tryAcquire 非阻塞占用一个并发槽，槽位已满返回 false。
func (p *Pool) tryAcquire() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running >= p.max {
		return false
	}
	p.running++
	return true
}

// release 归还并发槽（不唤醒调度器，由调用方决定是否需要 Enqueue）。
func (p *Pool) release() {
	p.mu.Lock()
	p.running--
	p.mu.Unlock()
}

func (p *Pool) loop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-ticker.C:
		}
		p.dispatch(ctx)
	}
}

// dispatch 尽量填满空闲并发槽：每个槽认领一个 pending 任务并交给 worker。
func (p *Pool) dispatch(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	for {
		// 尝试获取一个并发槽（非阻塞）
		if !p.tryAcquire() {
			return // 槽位已满
		}

		// 找到最早的 pending 任务（Find 不在空结果时报错/打日志）
		var tasks []model.Task
		err := p.db.WithContext(ctx).
			Where("status = ?", model.StatusPending).
			Order("created_at asc").
			Limit(1).
			Find(&tasks).Error
		if err != nil {
			p.release()
			return
		}
		if len(tasks) == 0 {
			p.release() // 无 pending 任务，释放槽位
			return
		}
		task := tasks[0]

		// 原子认领：pending -> running
		res := p.db.WithContext(ctx).
			Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, model.StatusPending).
			Updates(map[string]interface{}{
				"status":     model.StatusRunning,
				"started_at": time.Now(),
			})
		if res.Error != nil {
			p.release()
			return
		}
		if res.RowsAffected == 0 {
			p.release() // 已被其他实例认领，尝试下一个
			continue
		}

		// 重新加载完整任务
		if err := p.db.WithContext(ctx).First(&task, task.ID).Error; err != nil {
			p.release()
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return
			}
			// 认领成功但读取失败：回滚为 pending，避免任务卡在 running 直到重启
			if uerr := p.db.WithContext(context.WithoutCancel(ctx)).
				Model(&model.Task{}).
				Where("id = ? AND status = ?", task.ID, model.StatusRunning).
				Updates(map[string]interface{}{"status": model.StatusPending}).
				Error; uerr != nil {
				log.Printf("任务 #%d 认领后回滚失败，将保持 running 直至重启恢复：%v", task.ID, uerr)
			}
			return
		}

		p.wg.Add(1)
		go func(t model.Task) {
			defer func() {
				p.release()
				p.wg.Done()
				p.Enqueue()
			}()
			p.runner.Run(ctx, &t)
		}(task)
	}
}
