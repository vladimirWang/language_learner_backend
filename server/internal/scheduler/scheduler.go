// Package scheduler 提供构建任务的串行锁与内存水位校验。
package scheduler

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wangfernando/language-learner/server/internal/memutil"
	"github.com/wangfernando/language-learner/server/internal/repo"
	"github.com/wangfernando/language-learner/server/internal/runner"
	"github.com/wangfernando/language-learner/server/internal/store"
)

var (
	// ErrBusy 表示已有构建任务在跑（同一时刻只允许一个）。
	ErrBusy = errors.New("another build is running")
	// ErrLowMemory 表示主机可用内存低于阈值，拒绝拉起构建任务。
	ErrLowMemory = errors.New("available memory below threshold")
)

// Scheduler 串行调度构建任务。
type Scheduler struct {
	store      *store.Store
	runner     *runner.Runner
	minAvailMB int64
	logger     *log.Logger
	repoURLs   map[string]string

	mu   sync.Mutex
	busy bool
}

// New 创建调度器。
func New(st *store.Store, rn *runner.Runner, minAvailMB int64, repoURLs map[string]string, logger *log.Logger) *Scheduler {
	return &Scheduler{store: st, runner: rn, minAvailMB: minAvailMB, repoURLs: repoURLs, logger: logger}
}

// Busy 返回当前是否有构建在跑。
func (s *Scheduler) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// AvailableMB 返回主机可用内存，0 表示当前平台无法读取。
func (s *Scheduler) AvailableMB() uint64 {
	return memutil.AvailableMB()
}

// Trigger 校验锁与水位后异步启动构建，立即返回版本号。
func (s *Scheduler) Trigger(lang, version string) (string, error) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return "", ErrBusy
	}
	s.busy = true
	s.mu.Unlock()

	if avail := memutil.AvailableMB(); avail > 0 && int64(avail) < s.minAvailMB {
		s.setIdle()
		return "", ErrLowMemory
	}
	if version == "" {
		version = time.Now().Format("20060102-150405")
	}

	ctx := context.Background()
	s.logLine(ctx, version, "trigger", "accepted build lang="+lang)
	go s.run(ctx, version, lang)
	return version, nil
}

// TriggerListening 校验锁与水位后异步启动听力导入任务，立即返回 job_id。
// builder.listen 的 CLI args 由 handler 构造好传进来。
func (s *Scheduler) TriggerListening(jobID, examID, lang string, args []string, extraEnv map[string]string) error {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return ErrBusy
	}
	s.busy = true
	s.mu.Unlock()

	if avail := memutil.AvailableMB(); avail > 0 && int64(avail) < s.minAvailMB {
		s.setIdle()
		return ErrLowMemory
	}

	ctx := context.Background()
	go s.runListening(ctx, jobID, examID, lang, args, extraEnv)
	return nil
}

func (s *Scheduler) runListening(ctx context.Context, jobID, examID, lang string, args []string, extraEnv map[string]string) {
	defer s.setIdle()

	if s.store != nil {
		if err := s.store.StartListeningJob(ctx, jobID); err != nil {
			s.logger.Printf("start listening job %s: %v", jobID, err)
		}
		_ = s.store.AppendListeningLog(ctx, jobID, "init", "info",
			"starting listening import exam="+examID+" lang="+lang)
	}

	onLine := func(line string) {
		s.logger.Printf("[listening %s] %s", jobID, line)
		if s.store != nil {
			level := "info"
			stage := "builder"
			upper := strings.ToUpper(line)
			if strings.Contains(upper, "ERROR") || strings.Contains(upper, "Traceback") {
				level = "error"
			} else if strings.Contains(upper, "WARNING") {
				level = "warn"
			}
			_ = s.store.AppendListeningLog(ctx, jobID, stage, level, line)
		}
	}

	err := s.runner.RunCustom(ctx, args, extraEnv, onLine)
	if s.store == nil {
		return
	}
	if err != nil {
		s.logger.Printf("listening job %s failed: %v", jobID, err)
		_ = s.store.AppendListeningLog(ctx, jobID, "runner", "error", err.Error())
		_ = s.store.FailListeningJob(ctx, jobID, err.Error())
		return
	}
	// 成功：manifest 路径由 builder.listen 写入 work_root/manifest.json；
	// 这里只把状态置为 built，manifest_path 留空（客户端后续通过扫描目录拿）。
	_ = s.store.AppendListeningLog(ctx, jobID, "runner", "info", "builder.listen finished")
	_ = s.store.FinishListeningJob(ctx, jobID, "")
}

func (s *Scheduler) run(ctx context.Context, version, lang string) {
	defer s.setIdle()

	if s.store != nil {
		if err := s.store.StartBuild(ctx, version, lang); err != nil {
			s.logger.Printf("start build %s: %v", version, err)
		}
	}

	// 尝试获取该语种仓库的最新 commit，作为构建来源标记。
	sourceCommit := s.fetchCommit(ctx, lang)
	extraEnv := map[string]string{}
	if sourceCommit != "" {
		extraEnv["SOURCE_COMMIT"] = sourceCommit
		s.logLine(ctx, version, "trigger", "source_commit="+sourceCommit)
	}

	err := s.runner.Run(ctx, lang, version, extraEnv, func(line string) {
		s.logger.Printf("[builder %s] %s", version, line)
		s.logLine(ctx, version, "builder", line)
	})
	if err != nil {
		s.logger.Printf("build %s failed: %v", version, err)
		s.logLine(ctx, version, "runner", "error: "+err.Error())
		if s.store != nil {
			if e := s.store.MarkFailed(ctx, version); e != nil {
				s.logger.Printf("mark failed %s: %v", version, e)
			}
		}
		return
	}
	// 构建结果（built/failed、产物地址与 md5）由 Python 任务写入 build_versions。
	s.logLine(ctx, version, "runner", "builder finished")
}

func (s *Scheduler) setIdle() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}

func (s *Scheduler) logLine(ctx context.Context, version, stage, message string) {
	if s.store == nil {
		return
	}
	if err := s.store.AppendLog(ctx, version, stage, "info", message); err != nil {
		s.logger.Printf("append log: %v", err)
	}
}

// fetchCommit 查询该语种仓库的最新 commit hash；未配置或查询失败返回空串。
func (s *Scheduler) fetchCommit(ctx context.Context, lang string) string {
	url, ok := s.repoURLs[lang]
	if !ok || url == "" {
		return ""
	}
	commit, err := repo.LatestCommit(ctx, url)
	if err != nil {
		s.logger.Printf("fetch commit for %s: %v", lang, err)
		return ""
	}
	return commit
}
