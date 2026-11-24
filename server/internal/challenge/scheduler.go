package challenge

import (
	"context"
	"log"
	"time"
)

// Scheduler 启动时补当天和次日关卡，之后每天按配置时分（APP_TZ）生成次日关卡。
type Scheduler struct {
	builder      *Builder
	loc          *time.Location
	logger       *log.Logger
	generateHour int
	generateMin  int
}

// NewScheduler 创建每日关卡定时任务。
func NewScheduler(builder *Builder, loc *time.Location, logger *log.Logger, hour, minute int) *Scheduler {
	if loc == nil {
		loc = time.Local
	}
	return &Scheduler{builder: builder, loc: loc, logger: logger, generateHour: hour, generateMin: minute}
}

// Start 在后台运行定时任务；ctx 取消后退出。
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Scheduler) loop(ctx context.Context) {
	now := time.Now().In(s.loc)
	// 启动时补齐当天与次日；生成成功不代表未来关卡已提前对学员开放。
	s.generate(ctx, now)
	s.generate(ctx, now.AddDate(0, 0, 1))
	for {
		next := s.nextRun(time.Now().In(s.loc))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.generate(ctx, time.Now().In(s.loc).AddDate(0, 0, 1))
		}
	}
}

// nextRun 返回下一个配置时分。
func (s *Scheduler) nextRun(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), s.generateHour, s.generateMin, 0, 0, s.loc)
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
