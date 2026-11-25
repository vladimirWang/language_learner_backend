// version-service 是常驻的素材版本服务：版本清单、构建触发、构建状态。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wangfernando/language-learner/server/internal/challenge"
	"github.com/wangfernando/language-learner/server/internal/config"
	"github.com/wangfernando/language-learner/server/internal/handler"
	"github.com/wangfernando/language-learner/server/internal/llm"
	"github.com/wangfernando/language-learner/server/internal/mq"
	"github.com/wangfernando/language-learner/server/internal/pdfworker"
	"github.com/wangfernando/language-learner/server/internal/runner"
	"github.com/wangfernando/language-learner/server/internal/scheduler"
	"github.com/wangfernando/language-learner/server/internal/storage"
	"github.com/wangfernando/language-learner/server/internal/store"
	"github.com/wangfernando/language-learner/server/internal/streak"
	"github.com/wangfernando/language-learner/server/internal/tts"
	"github.com/wangfernando/language-learner/server/internal/wxauth"
)

func main() {
	logger := log.New(os.Stdout, "[version-service] ", log.LstdFlags|log.LUTC)
	cfg := config.Load()
	ctx := context.Background()

	st, err := store.New(ctx, cfg.PGDSN, cfg.PGMaxConns)
	if err != nil {
		logger.Fatalf("startup check failed: PostgreSQL initialization error: %v", err)
	}
	defer st.Close()
	if err := st.Ping(ctx); err != nil {
		logger.Fatalf("startup check failed: PostgreSQL is unavailable, application will not start: %v", err)
	}
	logger.Printf("startup check: PostgreSQL connected")

	publisher := mq.NewPublisher(cfg.RabbitMQURL, cfg.PDFQueue, logger)
	defer publisher.Close()
	if err := publisher.Check(); err != nil {
		logger.Fatalf("startup check failed: RabbitMQ is unavailable (queue=%s), application will not start: %v", cfg.PDFQueue, err)
	}
	logger.Printf("startup check: RabbitMQ connected (queue=%s)", cfg.PDFQueue)

	streakStore := streak.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer streakStore.Close()
	if err := streakStore.Ping(ctx); err != nil {
		logger.Fatalf("startup check failed: Redis is unavailable (addr=%s), application will not start: %v", cfg.RedisAddr, err)
	}
	logger.Printf("startup check: Redis connected (addr=%s)", cfg.RedisAddr)
	if days, err := st.PassedChallengeDays(ctx); err != nil {
		logger.Printf("streak backfill: %v", err)
	} else {
		history := make([]streak.Day, 0, len(days))
		for _, d := range days {
			history = append(history, streak.Day{OpenID: d.OpenID, Lang: d.Lang, Day: d.Day})
		}
		if n, err := streakStore.Backfill(ctx, history); err != nil {
			logger.Printf("streak backfill: %v", err)
		} else if n > 0 {
			logger.Printf("streak backfill: %d check-in days loaded", n)
		}
	}

	presigner, err := storage.NewPresigner(cfg.S3Endpoint, cfg.S3Bucket, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3UseSSL)
	if err != nil {
		logger.Fatalf("init object storage: %v", err)
	}

	rn := runner.New(cfg.BuilderCmd, cfg.BuilderArgs, cfg.BuilderDir)
	sch := scheduler.New(st, rn, cfg.MinAvailMB, cfg.RepoURLs, logger)
	llmClient := llm.NewFromEnv()
	if llmClient.Enabled() {
		logger.Printf("speaking dialog: LLM enabled")
	} else {
		logger.Printf("speaking dialog: LLM not configured, using templates")
	}
	ttsClient := tts.NewFromEnv()
	if ttsClient.Enabled() {
		logger.Printf("speaking tts: enabled")
	} else {
		logger.Printf("speaking tts: not configured, audio playback disabled")
	}
	// 恢复上次进程退出时未完成的解析任务，避免任务停在 queued/running。
	if ids, err := st.RequeueStalePDFJobs(ctx); err != nil {
		logger.Printf("requeue stale pdf jobs: %v", err)
	} else {
		for _, jobID := range ids {
			pubCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := publisher.Publish(pubCtx, map[string]string{"job_id": jobID})
			cancel()
			if err != nil {
				logger.Printf("requeue pdf job %s: %v", jobID, err)
			} else {
				logger.Printf("requeued pdf job %s", jobID)
			}
		}
	}
	pdfWorker := pdfworker.New(st, rn, cfg.RabbitMQURL, cfg.PDFQueue, cfg.WorkDir, logger)
	pdfWorker.Start(ctx)

	loc, err := time.LoadLocation(cfg.AppTZ)
	if err != nil {
		logger.Printf("invalid APP_TZ %q, fallback to local: %v", cfg.AppTZ, err)
		loc = time.Local
	}
	if cfg.DailyWordsAPIKey == "" {
		logger.Printf("daily words: DAILY_WORDS_API_KEY not configured, endpoints return 503")
	}
	dailyCfg := handler.DailyWordsConfig{
		APIKey:    cfg.DailyWordsAPIKey,
		RateLimit: cfg.DailyWordsRateLimit,
		Location:  loc,
	}
	// 每日关卡：启动时补当天和次日；之后在配置时刻生成次日，学员仍须到当天 00:00 才可见。
	generateAt, err := time.Parse("15:04", cfg.DailyChallengeGenerateAt)
	if err != nil || generateAt.Format("15:04") != cfg.DailyChallengeGenerateAt {
		logger.Fatalf("invalid DAILY_CHALLENGE_GENERATE_AT %q, want HH:mm", cfg.DailyChallengeGenerateAt)
	}
	challenge.NewScheduler(challenge.NewBuilder(st), loc, logger, generateAt.Hour(), generateAt.Minute()).Start(ctx)
	ssoCfg := handler.SSOConfig{
		BaseURL:       cfg.SSOBaseURL,
		PublicBaseURL: cfg.SSOPublicBaseURL,
		ClientID:      cfg.SSOClientID,
		ClientSecret:  cfg.SSOClientSecret,
		RedirectURI:   cfg.SSORedirectURI,
		DevMock:       cfg.SSODevMock,
		DevMockEmail:  cfg.SSODevMockEmail,
	}
	if ssoCfg.DevMock {
		logger.Printf("admin sso: DEV MOCK enabled (user=%s)", ssoCfg.DevMockEmail)
	} else if ssoCfg.Enabled() {
		logger.Printf("admin sso: enabled (client=%s)", ssoCfg.ClientID)
	} else {
		logger.Printf("admin sso: not configured, /api/auth/sso/* returns 503")
	}
	wxCfg := wxauth.Config{
		AppID:       cfg.WXAppID,
		AppSecret:   cfg.WXAppSecret,
		TokenSecret: cfg.WXTokenSecret,
		BaseURL:     cfg.WXBaseURL,
	}
	if wxCfg.Enabled() {
		logger.Printf("wechat miniapp login: enabled (appid=%s)", wxCfg.AppID)
	} else {
		logger.Printf("wechat miniapp login: not configured, /api/auth/wx-* returns 503")
	}
	langCfg := handler.LangConfig{Default: cfg.DefaultLang, Supported: cfg.SupportedLangs}
	h := handler.New(st, sch, presigner, cfg.RepoURLs, llmClient, ttsClient, publisher, dailyCfg, ssoCfg, wxCfg, langCfg, streakStore, logger)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		// PDF 试卷可能很大，上传耗时可能超过 15 秒；过短的读超时会把请求体截断。
		ReadTimeout:  10 * time.Minute,
		WriteTimeout: 2 * time.Minute,
	}

	go func() {
		logger.Printf("listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
	logger.Printf("stopped")
}
