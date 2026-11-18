// Package pdfworker 消费 PDF 解析队列，拉起 Python 一次性任务并回写状态。
package pdfworker

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/wangfernando/language-learner/server/internal/runner"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// Worker 是常驻的队列消费者；一次只处理一条消息，解析在 Python 子进程中完成。
type Worker struct {
	store  *store.Store
	runner *runner.Runner
	url    string
	queue  string
	work   string
	logger *log.Logger
}

// New 创建消费者。
func New(st *store.Store, rn *runner.Runner, url, queue, workDir string, logger *log.Logger) *Worker {
	return &Worker{store: st, runner: rn, url: url, queue: queue, work: workDir, logger: logger}
}

// Start 在后台启动消费循环；RabbitMQ 不可用时持续重试。
func (w *Worker) Start(ctx context.Context) {
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			if err := w.consume(ctx); err != nil && ctx.Err() == nil {
				w.logger.Printf("pdf worker: %v; retrying in 5s", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()
}

func (w *Worker) consume(ctx context.Context) error {
	conn, err := amqp.Dial(w.url)
	if err != nil {
		return err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	if err := ch.Qos(1, 0, false); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(w.queue, true, false, false, false, nil); err != nil {
		return err
	}
	msgs, err := ch.Consume(w.queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	w.logger.Printf("pdf worker consuming queue=%s", w.queue)

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-msgs:
			if !ok {
				return errors.New("delivery channel closed")
			}
			w.handle(ctx, msg)
		}
	}
}

func (w *Worker) handle(ctx context.Context, msg amqp.Delivery) {
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(msg.Body, &payload); err != nil || payload.JobID == "" {
		w.logger.Printf("pdf worker: invalid message: %v", err)
		_ = msg.Nack(false, false)
		return
	}

	job, err := w.store.GetPDFImportJob(ctx, payload.JobID)
	if err != nil {
		w.logger.Printf("pdf worker: load job %s: %v", payload.JobID, err)
		_ = msg.Nack(false, true)
		return
	}
	if job == nil {
		w.logger.Printf("pdf worker: job %s not found", payload.JobID)
		_ = msg.Ack(false)
		return
	}
	if job.Status == "running" || job.Status == "succeeded" {
		_ = msg.Ack(false)
		return
	}

	if err := w.store.StartPDFImportJob(ctx, job.JobID); err != nil {
		w.logger.Printf("pdf worker: start job %s: %v", job.JobID, err)
		_ = msg.Nack(false, true)
		return
	}
	_ = w.store.AppendPDFImportLog(ctx, job.JobID, "info", "开始解析 PDF: "+job.OriginalName)

	outPath := filepath.Join(w.work, "pdf", job.JobID, "result.json")
	args := []string{
		"run", "--no-sync", "python", "-m", "builder.pdf_import",
		"--job-id", job.JobID,
		"--pdf", job.StoredPath,
		"--lang", job.Lang,
		"--out", outPath,
	}

	pages := 0
	var result json.RawMessage
	runErr := w.runner.RunCustom(ctx, args, nil, func(line string) {
		level := "info"
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "ERROR") || strings.Contains(upper, "TRACEBACK") {
			level = "error"
		} else if strings.Contains(upper, "WARN") {
			level = "warn"
		}
		_ = w.store.AppendPDFImportLog(ctx, job.JobID, level, line)
		w.logger.Printf("[pdf %s] %s", job.JobID, line)

		if raw, ok := strings.CutPrefix(line, "RESULT_JSON: "); ok {
			var summary struct {
				PageCount int `json:"page_count"`
			}
			if err := json.Unmarshal([]byte(raw), &summary); err == nil {
				pages = summary.PageCount
			}
			result = json.RawMessage(raw)
		}
	})

	if runErr != nil {
		w.logger.Printf("pdf worker: job %s failed: %v", job.JobID, runErr)
		_ = w.store.AppendPDFImportLog(ctx, job.JobID, "error", runErr.Error())
		_ = w.store.FinishPDFImportJob(ctx, job.JobID, "failed", runErr.Error(), pages, nil)
		_ = msg.Ack(false)
		return
	}
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	if err := w.store.FinishPDFImportJob(ctx, job.JobID, "succeeded", "解析完成", pages, result); err != nil {
		w.logger.Printf("pdf worker: finish job %s: %v", job.JobID, err)
	}
	_ = w.store.AppendPDFImportLog(ctx, job.JobID, "info", "解析完成")
	_ = msg.Ack(false)
}
