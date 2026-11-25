// Package scheduler 提供构建任务的串行锁与内存水位校验。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wangfernando/language-learner/server/internal/memutil"
	"github.com/wangfernando/language-learner/server/internal/repo"
	"github.com/wangfernando/language-learner/server/internal/runner"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// pyLogLine 匹配 Python logging 默认格式：时间戳 级别 模块 消息。
var pyLogLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d+ (DEBUG|INFO|WARNING|ERROR|CRITICAL) `)

// parseLogLevel 从日志行解析级别。优先匹配 Python logging 的级别字段，
// 匹配不到时仅把含 Traceback 的行判为 error（避免消息里的 "error" 字样误报）。
func parseLogLevel(line string) string {
	if m := pyLogLine.FindStringSubmatch(line); m != nil {
		switch m[1] {
		case "ERROR", "CRITICAL":
			return "error"
		case "WARNING":
			return "warn"
		default:
			return "info"
		}
	}
	if strings.Contains(line, "Traceback") {
		return "error"
	}
	return "info"
}

var (
	// ErrBusy 表示已有构建任务在跑（同一时刻只允许一个）。
	ErrBusy = errors.New("another build is running")
	// ErrLowMemory 表示主机可用内存低于阈值，拒绝拉起构建任务。
	ErrLowMemory = errors.New("available memory below threshold")
)

// ListeningNotifier 由 handler 注入：听力解析任务阶段变化时用（持久化通知 + 推 SSE）。
type ListeningNotifier func(jobID, examID, stage, message string)

// Scheduler 串行调度构建任务。
type Scheduler struct {
	store      *store.Store
	runner     *runner.Runner
	minAvailMB int64
	logger     *log.Logger
	repoURLs   map[string]string
	notifier   ListeningNotifier

	mu   sync.Mutex
	busy bool
}

// New 创建调度器。
func New(st *store.Store, rn *runner.Runner, minAvailMB int64, repoURLs map[string]string, logger *log.Logger) *Scheduler {
	return &Scheduler{store: st, runner: rn, minAvailMB: minAvailMB, repoURLs: repoURLs, logger: logger}
}

// SetListeningNotifier 设置听力任务阶段变化回调。
func (s *Scheduler) SetListeningNotifier(fn ListeningNotifier) {
	s.notifier = fn
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
	// 同步登记构建记录，避免前端紧接着查状态时记录尚未入库而 404。
	if s.store != nil {
		if err := s.store.StartBuild(ctx, version, lang); err != nil {
			s.logger.Printf("start build %s: %v", version, err)
		}
	}
	s.logLine(ctx, version, "trigger", "accepted build lang="+lang)
	go s.run(ctx, version, lang)
	return version, nil
}

// TriggerListening 校验锁与水位后异步启动听力导入任务，立即返回 job_id。
// builder.listen 的 CLI args 由 handler 构造好传进来。
func (s *Scheduler) TriggerListening(jobID, examID, lang string, args []string, extraEnv map[string]string) error {
	return s.triggerListening(jobID, examID, lang, s.runner, args, extraEnv)
}

// TriggerListeningBin 使用独立 listen-parser 二进制执行解析（LISTEN_BIN）。
// 与 Python 入口共用同一 CLI 契约，args 不需要 `run --no-sync python -m builder.listen` 前缀。
func (s *Scheduler) TriggerListeningBin(jobID, examID, lang, bin string, args []string, extraEnv map[string]string) error {
	return s.triggerListening(jobID, examID, lang, runner.New(bin, nil, ""), args, extraEnv)
}

func (s *Scheduler) triggerListening(jobID, examID, lang string, rn *runner.Runner, args []string, extraEnv map[string]string) error {
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
	go s.runListening(ctx, jobID, examID, lang, rn, args, extraEnv)
	return nil
}

func (s *Scheduler) runListening(ctx context.Context, jobID, examID, lang string, rn *runner.Runner, args []string, extraEnv map[string]string) {
	defer s.setIdle()

	if s.store != nil {
		if err := s.store.StartListeningJob(ctx, jobID); err != nil {
			s.logger.Printf("start listening job %s: %v", jobID, err)
		}
		_ = s.store.AppendListeningLog(ctx, jobID, "init", "info",
			"starting listening import exam="+examID+" lang="+lang)
	}
	s.notify(jobID, examID, "queued", "任务已创建，等待下载")

	onLine := func(line string) {
		s.logger.Printf("[listening %s] %s", jobID, line)
		// builder 输出的机器可读阶段标记：STAGE {"stage":"downloading","message":"..."}
		if stage, msg, ok := parseStageMarker(line); ok {
			s.notify(jobID, examID, stage, msg)
			return
		}
		if s.store != nil {
			level := parseLogLevel(line)
			stage := "builder"
			_ = s.store.AppendListeningLog(ctx, jobID, stage, level, line)
		}
	}

	err := rn.RunCustom(ctx, args, extraEnv, onLine)
	if s.store == nil {
		return
	}
	if err != nil {
		s.logger.Printf("listening job %s failed: %v", jobID, err)
		_ = s.store.AppendListeningLog(ctx, jobID, "runner", "error", err.Error())
		_ = s.store.FailListeningJob(ctx, jobID, err.Error())
		s.notify(jobID, examID, "failed", err.Error())
		return
	}
	// 成功：manifest 路径由 builder.listen 写入 work_root/manifest.json；
	// 这里只把状态置为 built，manifest_path 留空（客户端后续通过扫描目录拿）。
	_ = s.store.AppendListeningLog(ctx, jobID, "runner", "info", "builder.listen finished")
	_ = s.store.FinishListeningJob(ctx, jobID, "")
	s.notify(jobID, examID, "completed", "解析完成，可查看产物与日志")
}

// stageMarkerPrefix 是 builder 输出的阶段标记前缀。
const stageMarkerPrefix = "STAGE "

// parseStageMarker 解析 `STAGE {"stage":"downloading","message":"..."}` 行。
func parseStageMarker(line string) (stage, message string, ok bool) {
	if !strings.HasPrefix(line, stageMarkerPrefix) {
		return "", "", false
	}
	var payload struct {
		Stage   string `json:"stage"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, stageMarkerPrefix)), &payload); err != nil || payload.Stage == "" {
		return "", "", false
	}
	return payload.Stage, payload.Message, true
}

// notify 落一条状态日志，并把阶段变化交给 handler（写通知 + 推 SSE）。
func (s *Scheduler) notify(jobID, examID, stage, message string) {
	if s.notifier == nil {
		return
	}
	if s.store != nil && message != "" {
		_ = s.store.AppendListeningLog(context.Background(), jobID, "status", "info", message)
	}
	s.notifier(jobID, examID, stage, message)
}

func (s *Scheduler) run(ctx context.Context, version, lang string) {
	defer s.setIdle()

	// 尝试获取该语种仓库的最新 commit，作为构建来源标记。
	sourceCommit := s.fetchCommit(ctx, lang)
	extraEnv := map[string]string{}
	if sourceCommit != "" {
		extraEnv["SOURCE_COMMIT"] = sourceCommit
		s.logLine(ctx, version, "trigger", "source_commit="+sourceCommit)
	}

	var result *buildResult
	err := s.runner.Run(ctx, lang, version, extraEnv, func(line string) {
		s.logger.Printf("[builder %s] %s", version, line)
		if payload, ok := parseBuildResult(line); ok {
			result = &payload
			s.logLine(ctx, version, "builder", "build result received")
			return
		}
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
	// 构建结果由 builder 以 BUILD_RESULT 行输出，Go 负责落库。
	if s.store != nil {
		if result == nil {
			s.logLine(ctx, version, "runner", "error: builder produced no BUILD_RESULT")
			if e := s.store.MarkFailed(ctx, version); e != nil {
				s.logger.Printf("mark failed %s: %v", version, e)
			}
			return
		}
		if e := s.store.FinishBuild(ctx, version,
			result.DBURL, result.VectorSnapshotURL, result.DBMd5, result.VectorMd5); e != nil {
			s.logger.Printf("finish build %s: %v", version, e)
		}
	}
	s.logLine(ctx, version, "runner", "builder finished")
}

// buildResult 是 builder 输出的构建产物信息。
type buildResult struct {
	Version           string `json:"version"`
	Lang              string `json:"lang"`
	DBURL             string `json:"db_url"`
	VectorSnapshotURL string `json:"vector_snapshot_url"`
	DBMd5             string `json:"db_md5"`
	VectorMd5         string `json:"vector_md5"`
}

const buildResultPrefix = "BUILD_RESULT "

func parseBuildResult(line string) (buildResult, bool) {
	if !strings.HasPrefix(line, buildResultPrefix) {
		return buildResult{}, false
	}
	var r buildResult
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, buildResultPrefix)), &r); err != nil {
		return buildResult{}, false
	}
	return r, true
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
