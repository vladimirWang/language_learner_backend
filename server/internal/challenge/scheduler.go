package challenge

import (
	"context"
	"log"
	"time"
)

// challengeBufferSize 是每个 (lang, level) 预生成的已发布关卡数量，保证学员解锁推进时序列不枯竭。
const challengeBufferSize = 30

// Scheduler 定时维护关卡缓冲：每个 (lang, level) 保持 challengeBufferSize 个已发布关卡。
type Scheduler struct {
	builder *Builder
	logger  *log.Logger
}

// NewScheduler 创建关卡缓冲维护任务；hour/minute 参数保留以兼容旧调用方，当前不再按天定时。
func NewScheduler(builder *Builder, _ *time.Location, logger *log.Logger, _, _ int) *Scheduler {
	return &Scheduler{builder: builder, logger: logger}
}

// Start 在后台运行定时任务；ctx 取消后退出。
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Scheduler) loop(ctx context.Context) {
	s.ensureBuffer(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ensureBuffer(ctx)
		}
	}
}

func (s *Scheduler) ensureBuffer(ctx context.Context) {
	for _, err := range s.builder.EnsureCountAll(ctx, challengeBufferSize) {
		if s.logger != nil {
			s.logger.Printf("daily challenge ensure buffer: %v", err)
		}
	}
}
