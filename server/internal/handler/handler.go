// Package handler 暴露版本清单、构建触发与构建状态 HTTP 接口。
package handler

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/llm"
	"github.com/wangfernando/language-learner/server/internal/scheduler"
	"github.com/wangfernando/language-learner/server/internal/storage"
	"github.com/wangfernando/language-learner/server/internal/store"
)

var langPattern = regexp.MustCompile(`^[a-z]{2,5}$`)

// JobPublisher 抽象异步任务投递，便于在未配置 MQ 时降级。
type JobPublisher interface {
	Publish(ctx context.Context, payload any) error
}

// Handler 聚合依赖并构造路由。
type Handler struct {
	store     *store.Store
	sched     *scheduler.Scheduler
	presigner *storage.Presigner
	repoURLs  map[string]string
	llm       *llm.Client
	pdfPub    JobPublisher
	logger    *log.Logger

	// 口语兜底话术缓存：启动/读写后按 TTL 重新加载，避免每次追问都查库。
	tplMu      sync.RWMutex
	tplCache   map[string][]store.SpeakingReplyTemplate
	tplCacheAt time.Time

	// 主题对话编排缓存：同一主题在 TTL 内复用同一份 ≥5 轮脚本。
	planMu    sync.RWMutex
	planCache map[string]topicPlanResp
	planAt    time.Time
}

// New 创建 HTTP 处理器。
func New(st *store.Store, sch *scheduler.Scheduler, ps *storage.Presigner, repoURLs map[string]string, llmClient *llm.Client, pdfPub JobPublisher, logger *log.Logger) *Handler {
	return &Handler{store: st, sched: sch, presigner: ps, repoURLs: repoURLs, llm: llmClient, pdfPub: pdfPub, logger: logger}
}

// Routes 注册全部 API 路由。gin.Engine 实现了 http.Handler，签名保持不变。
func (h *Handler) Routes() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// 中间件顺序与原实现一致：最外层记录日志，其次处理 CORS。
	r.Use(h.logRequests(), h.cors())
	// 与 net/http ServeMux 保持一致：关闭尾斜杠/路径自动纠正重定向，路径存在但方法不匹配时返回 405。
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.HandleMethodNotAllowed = true

	api := r.Group("/api")
	{
		// 基础：健康检查、版本清单、素材下载、仓库状态
		api.GET("/health", h.health)
		api.GET("/version", h.version)
		api.GET("/material/:lang/:file", h.material)
		api.GET("/repos", h.repos)

		// 构建触发与构建状态
		api.POST("/build/trigger", h.trigger)
		api.GET("/build/status", h.buildStatus)

		// PDF 试卷导入：分片上传支持断点续传，合并后再投递解析队列
		api.POST("/pdf/upload/init", h.pdfUploadInit)
		api.GET("/pdf/upload/:uploadId", h.pdfUploadStatus)
		api.PUT("/pdf/upload/:uploadId/chunk/:index", h.pdfUploadChunk)
		api.POST("/pdf/upload/:uploadId/complete", h.pdfUploadComplete)
		// 单次上传保留给脚本/调试使用
		api.POST("/pdf/import", h.pdfImport)
		api.GET("/pdf/jobs", h.pdfJobs)
		api.GET("/pdf/job/:jobId", h.pdfJobDetail)
		api.GET("/pdf/job/:jobId/blocks", h.pdfJobBlocks)
		api.PUT("/pdf/job/:jobId/blocks", h.pdfUpdateBlocks)
		api.POST("/pdf/job/:jobId/retry", h.pdfJobRetry)
		api.DELETE("/pdf/job/:jobId", h.pdfJobDelete)
		api.POST("/pdf/job/:jobId/restore", h.pdfJobRestore)
	}

	// 德专四级（PGG）结构化试题：数据来自已解析任务的 result.json。
	exam := api.Group("/exam")
	{
		exam.GET("/papers", h.listExamPapers)
		exam.GET("/paper/:paperId/questions", h.listExamQuestions)
		exam.GET("/paper/:paperId/diktat", h.listExamDiktat)
		exam.GET("/paper/:paperId/writing", h.listExamWriting)
		exam.GET("/question", h.getExamQuestion)
		exam.GET("/material", h.examMaterial)
		exam.POST("/answer", h.submitExamAnswer)
		// 管理台题库维护：浏览、绑定答案
		exam.GET("/admin/papers", h.examAdminPapers)
		exam.GET("/admin/questions", h.examAdminQuestions)
		exam.PUT("/admin/answer", h.examAdminUpdateAnswer)
	}

	// 听力导入任务
	listening := api.Group("/listening")
	{
		listening.POST("/import", h.listeningImport)
		listening.GET("/jobs", h.listeningJobs)
		listening.GET("/job/:jobId", h.listeningJobDetail)
		listening.GET("/job/:jobId/files", h.listeningFiles)
		listening.GET("/job/:jobId/download", h.listeningDownload)
		listening.GET("/job/:jobId/archive", h.listeningArchive)
		listening.GET("/job/:jobId/answers", h.listeningJobAnswers)
		listening.POST("/job/:jobId/answers", h.listeningUpdateAnswers)
		listening.PATCH("/job/:jobId", h.updateListeningJobMeta)
		listening.DELETE("/job/:jobId", h.deleteListeningJob)
	}

	// 关卡配置
	levels := api.Group("/levels")
	{
		levels.GET("", h.listLevels)
		levels.POST("", h.createLevel)
		levels.GET("/:id", h.getLevel)
		levels.PUT("/:id", h.updateLevel)
		levels.DELETE("/:id", h.deleteLevel)
		levels.GET("/:id/exams", h.listLevelExams)
		levels.POST("/:id/exams", h.addLevelExam)
		levels.DELETE("/:id/exams/:examId", h.removeLevelExam)
		levels.GET("/:id/exams/available", h.listAvailableExams)
	}

	// 口语题库与兜底话术
	speaking := api.Group("/speaking")
	{
		speaking.GET("/items", h.listSpeakingItems)
		speaking.GET("/exam-parts", h.listSpeakingExamParts)
		speaking.POST("/exam-parts", h.createSpeakingExamPart)
		speaking.PUT("/exam-parts/:id", h.updateSpeakingExamPart)
		speaking.DELETE("/exam-parts/:id", h.deleteSpeakingExamPart)
		speaking.GET("/plans", h.listSpeakingPlans)
		speaking.GET("/plans/:id", h.getSpeakingPlan)
		speaking.POST("/plans", h.createSpeakingPlan)
		speaking.POST("/plans/generate", h.generateSpeakingPlan)
		speaking.PUT("/plans/:id", h.updateSpeakingPlan)
		speaking.DELETE("/plans/:id", h.deleteSpeakingPlan)
		speaking.GET("/items/:key", h.getSpeakingItem)
		speaking.POST("/items", h.createSpeakingItem)
		speaking.PUT("/items/:key", h.updateSpeakingItem)
		speaking.DELETE("/items/:key", h.deleteSpeakingItem)
		speaking.POST("/reply", h.speakingReply)
		speaking.POST("/check", h.speakingCheck)
		speaking.POST("/transcribe", h.speakingTranscribe)
		speaking.POST("/topic-plan", h.speakingTopicPlan)
		speaking.POST("/validate-item", h.validateSpeakingItem)
		speaking.GET("/templates", h.listSpeakingTemplates)
		speaking.GET("/topics", h.listSpeakingTopics)
		speaking.POST("/templates", h.createSpeakingTemplate)
		speaking.PUT("/templates/:id", h.updateSpeakingTemplate)
		speaking.DELETE("/templates/:id", h.deleteSpeakingTemplate)
	}
	return r
}

// cors 允许跨域请求（供 Tauri client 端访问）。
// TODO: 后续可改为 Tauri Rust HTTP 命令，由 Rust 端请求后端，彻底绕开浏览器跨域，
// 届时可移除此中间件。
func (h *Handler) cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func (h *Handler) logRequests() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		h.logger.Printf("%s %s %d %s", c.Request.Method, c.Request.URL.RequestURI(), c.Writer.Status(), time.Since(start))
	}
}

func writeJSON(c *gin.Context, status int, payload any) {
	c.JSON(status, payload)
}

func writeError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, map[string]string{"error": message})
}
