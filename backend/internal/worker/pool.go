package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
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

	// 取消控制：每个正在执行的任务持有一个可取消的 ctx，CancelTask 通过 cancel 函数
	// 打断其 IO；cancelled 记录用户主动取消的任务，供 runner 区分「用户取消」与「服务中断」。
	cancelMu  sync.Mutex
	cancels   map[uint]context.CancelFunc
	cancelled map[uint]struct{}
}

func NewPool(db *gorm.DB, runner *Runner, concurrency int) *Pool {
	return &Pool{
		db:        db,
		runner:    runner,
		wake:      make(chan struct{}, 1),
		max:       setting.ClampConcurrency(concurrency),
		cancels:   make(map[uint]context.CancelFunc),
		cancelled: make(map[uint]struct{}),
	}
}

// SetRunner 在 runner 与 pool 互相引用时，于二者都构造完成后回填 runner。
// （NewPool 需 runner，NewRunner 需 pool 作为 CancelChecker，故先建 pool 再建 runner。）
func (p *Pool) SetRunner(r *Runner) {
	p.mu.Lock()
	p.runner = r
	p.mu.Unlock()
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
// 调度为纯事件驱动，这里先唤醒一次，确保启动前已存在的 pending 任务被立即拾起。
func (p *Pool) Start(ctx context.Context) {
	p.Enqueue()
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

// registerCancel 记录任务的取消函数（任务开始执行时调用）。
func (p *Pool) registerCancel(id uint, cancel context.CancelFunc) {
	p.cancelMu.Lock()
	p.cancels[id] = cancel
	p.cancelMu.Unlock()
}

// unregisterCancel 任务结束后清理：释放 ctx 并移除取消记录（避免泄漏）。
func (p *Pool) unregisterCancel(id uint) {
	p.cancelMu.Lock()
	if c, ok := p.cancels[id]; ok {
		c()
		delete(p.cancels, id)
	}
	delete(p.cancelled, id)
	p.cancelMu.Unlock()
}

// CancelTask 主动取消指定任务：仅当任务正在执行（已注册取消函数）时有效。
// 标记 cancelled 并触发其 ctx 取消；runner 收尾时据此清理临时文件并落库为 cancelled。
// 返回是否成功发送取消信号（false 表示任务已结束或尚未执行）。
func (p *Pool) CancelTask(id uint) bool {
	p.cancelMu.Lock()
	c, ok := p.cancels[id]
	if ok {
		p.cancelled[id] = struct{}{}
		c()
	}
	p.cancelMu.Unlock()
	return ok
}

// IsCancelled 判断任务是否由用户主动取消（runner 用以区分服务中断重排）。
func (p *Pool) IsCancelled(id uint) bool {
	p.cancelMu.Lock()
	_, ok := p.cancelled[id]
	p.cancelMu.Unlock()
	return ok
}

// loop 调度主循环：纯事件驱动（建任务 / 任务结束 / 调整并发 / 重试 / 手动唤醒），
// 不做定时轮询；若有任务卡在 pending，由配置页「唤醒任务队列」按钮兜底唤醒。
func (p *Pool) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
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
			// 任务已回到 pending：延迟唤醒一次去重调度，避免立即重试造成忙循环
			time.AfterFunc(2*time.Second, p.Enqueue)
			return
		}

		p.wg.Add(1)
		go func(t model.Task) {
			defer func() {
				if r := recover(); r != nil {
					// worker 协程内的 panic 不会被 gin.Recovery 捕获，会直接拖垮进程；
					// 这里兜底捕获、把堆栈落到标准日志，并把任务标记为失败，
					// 同时照常归还并发槽，避免该任务永久卡在 running 占用槽位。
					log.Printf("[PANIC] 任务 #%d 执行异常崩溃：%v\n%s", t.ID, r, debug.Stack())
					p.runner.fail(&t, fmt.Sprintf("任务执行异常崩溃：%v", r))
				}
				p.unregisterCancel(t.ID)
				p.release()
				p.wg.Done()
				p.Enqueue()
			}()
			taskCtx, cancel := context.WithCancel(ctx)
			p.registerCancel(t.ID, cancel)
			p.runner.Run(taskCtx, &t)
		}(task)
	}
}
