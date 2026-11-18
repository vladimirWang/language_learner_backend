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

	"github.com/wangfernando/language-learner/server/internal/config"
	"github.com/wangfernando/language-learner/server/internal/handler"
	"github.com/wangfernando/language-learner/server/internal/llm"
	"github.com/wangfernando/language-learner/server/internal/mq"
	"github.com/wangfernando/language-learner/server/internal/pdfworker"
	"github.com/wangfernando/language-learner/server/internal/runner"
	"github.com/wangfernando/language-learner/server/internal/scheduler"
	"github.com/wangfernando/language-learner/server/internal/storage"
	"github.com/wangfernando/language-learner/server/internal/store"
)

func main() {
	logger := log.New(os.Stdout, "[version-service] ", log.LstdFlags|log.LUTC)
	cfg := config.Load()
	ctx := context.Background()

	st, err := store.New(ctx, cfg.PGDSN, cfg.PGMaxConns)
	if err != nil {
		logger.Fatalf("init store: %v", err)
	}
	defer st.Close()

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
	publisher := mq.NewPublisher(cfg.RabbitMQURL, cfg.PDFQueue, logger)
	defer publisher.Close()
	pdfWorker := pdfworker.New(st, rn, cfg.RabbitMQURL, cfg.PDFQueue, cfg.WorkDir, logger)
	pdfWorker.Start(ctx)

	h := handler.New(st, sch, presigner, cfg.RepoURLs, llmClient, publisher, logger)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
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
