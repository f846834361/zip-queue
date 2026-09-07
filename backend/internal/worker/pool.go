package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"zip-queue/internal/model"
)

// Pool 管理并发 worker：从 DB 取 pending 任务、原子认领、分发给 runner。
type Pool struct {
	db     *gorm.DB
	runner *Runner
	sem    chan struct{}  // 并发槽（容量 = max_concurrent）
	wake   chan struct{}  // 非阻塞唤醒信号
	wg     sync.WaitGroup // 跟踪进行中的 worker
}

func NewPool(db *gorm.DB, runner *Runner, concurrency int) *Pool {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Pool{
		db:     db,
		runner: runner,
		sem:    make(chan struct{}, concurrency),
		wake:   make(chan struct{}, 1),
	}
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
	for {
		// 尝试获取一个并发槽（非阻塞）
		select {
		case p.sem <- struct{}{}:
		default:
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
			<-p.sem
			return
		}
		if len(tasks) == 0 {
			<-p.sem // 无 pending 任务，释放槽位
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
			<-p.sem
			return
		}
		if res.RowsAffected == 0 {
			<-p.sem // 已被其他实例认领，尝试下一个
			continue
		}

		// 重新加载完整任务
		if err := p.db.WithContext(ctx).First(&task, task.ID).Error; err != nil {
			<-p.sem
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return
			}
			return
		}

		p.wg.Add(1)
		go func(t model.Task) {
			defer func() {
				<-p.sem
				p.wg.Done()
				p.Enqueue()
			}()
			p.runner.Run(ctx, &t)
		}(task)
	}
}
