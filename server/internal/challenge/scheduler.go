package challenge

import (
	"context"
	"log"
	"time"
)

// Scheduler 每天 00:05（APP_TZ）生成本日关卡，并在启动时补生成当天内容。
type Scheduler struct {
	builder *Builder
	loc     *time.Location
	logger  *log.Logger
}

// NewScheduler 创建每日关卡定时任务。
func NewScheduler(builder *Builder, loc *time.Location, logger *log.Logger) *Scheduler {
	if loc == nil {
		loc = time.Local
	}
	return &Scheduler{builder: builder, loc: loc, logger: logger}
}

// Start 在后台运行定时任务；ctx 取消后退出。
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Scheduler) loop(ctx context.Context) {
	// 启动时先补一次当天内容，避免服务在 00:05 之后才启动时当天没有关卡。
	s.generate(ctx, time.Now().In(s.loc))
	for {
		next := s.nextRun(time.Now().In(s.loc))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.generate(ctx, time.Now().In(s.loc))
		}
	}
}

// nextRun 返回下一个 00:05。
func (s *Scheduler) nextRun(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 5, 0, 0, s.loc)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (s *Scheduler) generate(ctx context.Context, day time.Time) {
	for _, err := range s.builder.GenerateDay(ctx, day) {
		if s.logger != nil {
			s.logger.Printf("daily challenge generate %s: %v", day.Format("2006-01-02"), err)
		}
	}
}
