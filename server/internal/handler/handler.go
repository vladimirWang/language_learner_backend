// Package handler 暴露版本清单、构建触发与构建状态 HTTP 接口。
package handler

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/llm"
	"github.com/wangfernando/language-learner/server/internal/repo"
	"github.com/wangfernando/language-learner/server/internal/scheduler"
	"github.com/wangfernando/language-learner/server/internal/storage"
	"github.com/wangfernando/language-learner/server/internal/store"
)

var langPattern = regexp.MustCompile(`^[a-z]{2,5}$`)

// Handler 聚合依赖并构造路由。
type Handler struct {
	store     *store.Store
	sched     *scheduler.Scheduler
	presigner *storage.Presigner
	repoURLs  map[string]string
	llm       *llm.Client
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
func New(st *store.Store, sch *scheduler.Scheduler, ps *storage.Presigner, repoURLs map[string]string, llmClient *llm.Client, logger *log.Logger) *Handler {
	return &Handler{store: st, sched: sch, presigner: ps, repoURLs: repoURLs, llm: llmClient, logger: logger}
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
		speaking.GET("/items/:key", h.getSpeakingItem)
		speaking.POST("/items", h.createSpeakingItem)
		speaking.PUT("/items/:key", h.updateSpeakingItem)
		speaking.DELETE("/items/:key", h.deleteSpeakingItem)
		speaking.POST("/reply", h.speakingReply)
		speaking.POST("/check", h.speakingCheck)
		speaking.POST("/topic-plan", h.speakingTopicPlan)
		speaking.POST("/validate-item", h.validateSpeakingItem)
		speaking.GET("/templates", h.listSpeakingTemplates)
		speaking.GET("/topics", h.listSpeakingTopics)
		speaking.POST("/templates", h.createSpeakingTemplate)
		speaking.PUT("/templates/:id", h.updateSpeakingTemplate)
		speaking.DELETE("/templates/:id", h.deleteSpeakingTemplate)
		speaking.GET("/plans", h.listSpeakingPlans)
		speaking.GET("/plans/:id", h.getSpeakingPlan)
		speaking.POST("/plans", h.createSpeakingPlan)
		speaking.PUT("/plans/:id", h.updateSpeakingPlan)
		speaking.DELETE("/plans/:id", h.deleteSpeakingPlan)
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

func (h *Handler) health(c *gin.Context) {
	resp := map[string]any{
		"status":       "ok",
		"available_mb": h.sched.AvailableMB(),
		"build_busy":   h.sched.Busy(),
		"postgres":     "ok",
	}
	if err := h.store.Ping(c.Request.Context()); err != nil {
		resp["status"] = "degraded"
		resp["postgres"] = err.Error()
	}
	writeJSON(c, http.StatusOK, resp)
}

func (h *Handler) version(c *gin.Context) {
	lang := c.Query("lang")
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return
	}

	v, err := h.store.LatestVersion(c.Request.Context(), lang)
	if err != nil {
		h.logger.Printf("query version lang=%s: %v", lang, err)
		writeError(c, http.StatusInternalServerError, "query version failed")
		return
	}
	if v == nil {
		writeError(c, http.StatusNotFound, "no built version for lang "+lang)
		return
	}

	dbURL, vecURL, err := h.presign(c.Request.Context(), v.DBURL, v.VectorURL)
	if err != nil {
		h.logger.Printf("presign version=%s: %v", v.Version, err)
		writeError(c, http.StatusInternalServerError, "sign download url failed")
		return
	}
	// 未配置对象存储时，构建产物落在服务端本地目录：改由本服务直接提供下载。
	if strings.HasPrefix(dbURL, "/") {
		dbURL = "/api/material/" + lang + "/ground_truth.db"
	}
	if strings.HasPrefix(vecURL, "/") {
		vecURL = "/api/material/" + lang + "/vectors.jsonl"
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"lang":    v.Lang,
		"version": v.Version,
		"ground_truth": map[string]string{
			"url": dbURL,
			"md5": v.DBMD5,
		},
		"vector_snapshot": map[string]string{
			"url": vecURL,
			"md5": v.VectorMD5,
		},
		"created_at": v.CreatedAt,
	})
}

func (h *Handler) trigger(c *gin.Context) {
	var req struct {
		Lang    string `json:"lang"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	if !langPattern.MatchString(req.Lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return
	}

	version, err := h.sched.Trigger(req.Lang, req.Version)
	switch {
	case errors.Is(err, scheduler.ErrBusy):
		writeError(c, http.StatusConflict, err.Error())
		return
	case errors.Is(err, scheduler.ErrLowMemory):
		writeError(c, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		h.logger.Printf("trigger build: %v", err)
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(c, http.StatusAccepted, map[string]string{
		"lang":    req.Lang,
		"version": version,
		"status":  "building",
	})
}

func (h *Handler) buildStatus(c *gin.Context) {
	version := c.Query("version")
	if version == "" {
		writeError(c, http.StatusBadRequest, "missing version")
		return
	}
	v, logs, err := h.store.BuildStatus(c.Request.Context(), version)
	if err != nil {
		h.logger.Printf("query build status %s: %v", version, err)
		writeError(c, http.StatusInternalServerError, "query build status failed")
		return
	}
	if v == nil {
		writeError(c, http.StatusNotFound, "version not found")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"version":             v.Version,
		"lang":                v.Lang,
		"status":              v.Status,
		"db_url":              v.DBURL,
		"vector_snapshot_url": v.VectorURL,
		"db_md5":              v.DBMD5,
		"vector_md5":          v.VectorMD5,
		"created_at":          v.CreatedAt,
		"logs":                logs,
	})
}

func (h *Handler) material(c *gin.Context) {
	lang := c.Param("lang")
	name := c.Param("file")
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return
	}
	if name != "ground_truth.db" && name != "vectors.jsonl" {
		writeError(c, http.StatusNotFound, "unknown material file")
		return
	}

	v, err := h.store.LatestVersion(c.Request.Context(), lang)
	if err != nil {
		h.logger.Printf("query version lang=%s: %v", lang, err)
		writeError(c, http.StatusInternalServerError, "query version failed")
		return
	}
	if v == nil {
		writeError(c, http.StatusNotFound, "no built version for lang "+lang)
		return
	}

	path := v.DBURL
	if name == "vectors.jsonl" {
		path = v.VectorURL
	}
	// 只有落在本机构建目录的产物才能直出；对象存储 key 交给预签名 URL。
	if !filepath.IsAbs(path) || strings.Contains(path, "..") ||
		!strings.HasSuffix(filepath.Clean(path), "/"+lang+"/"+name) {
		writeError(c, http.StatusConflict, "material is not served as a local file")
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename="+name)
	http.ServeFile(c.Writer, c.Request, path)
}

func (h *Handler) presign(ctx context.Context, dbKey, vecKey string) (string, string, error) {
	dbURL, err := h.presigner.URL(ctx, dbKey)
	if err != nil {
		return "", "", err
	}
	vecURL, err := h.presigner.URL(ctx, vecKey)
	if err != nil {
		return "", "", err
	}
	return dbURL, vecURL, nil
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

// repos 返回各语种素材仓库的远程状态，以及与最近一次构建的对比。
func (h *Handler) repos(c *gin.Context) {
	result := make([]map[string]any, 0, len(h.repoURLs))
	for lang, url := range h.repoURLs {
		item := map[string]any{
			"lang":     lang,
			"repo_url": url,
		}

		// 查询远程最新 commit
		commit, err := repo.LatestCommit(c.Request.Context(), url)
		if err != nil {
			item["error"] = err.Error()
			item["latest_commit"] = ""
		} else {
			item["latest_commit"] = commit
			item["latest_commit_short"] = repo.ShortHash(commit)
		}

		// 查询最近一次构建的 source_commit
		lastVersion, err := h.store.LatestVersion(c.Request.Context(), lang)
		if err != nil {
			h.logger.Printf("query latest version lang=%s: %v", lang, err)
			lastVersion = nil
		}
		if lastVersion != nil {
			item["last_build_version"] = lastVersion.Version
			item["last_build_commit"] = lastVersion.SourceCommit
			item["last_build_at"] = lastVersion.CreatedAt
			// 判断是否有未构建的变更
			if commit != "" && lastVersion.SourceCommit != "" {
				item["pending"] = commit != lastVersion.SourceCommit
			} else if commit != "" && lastVersion.SourceCommit == "" {
				item["pending"] = true
			} else {
				item["pending"] = false
			}
		} else {
			item["last_build_version"] = nil
			item["last_build_commit"] = ""
			item["pending"] = commit != ""
		}

		result = append(result, item)
	}
	writeJSON(c, http.StatusOK, map[string]any{"repos": result})
}

// listeningImport 接收音视频 URL + 可选的 answers/cookies 文件，异步启动 builder.listen。
// multipart 字段：url(examId/lang/model) 文本 + answers/cookies 文件（可选）。
func (h *Handler) listeningImport(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(50 << 20); err != nil {
		writeError(c, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}

	url := strings.TrimSpace(c.Request.FormValue("url"))
	examID := strings.TrimSpace(c.Request.FormValue("examId"))
	lang := strings.TrimSpace(c.Request.FormValue("lang"))
	model := strings.TrimSpace(c.Request.FormValue("model"))

	if url == "" {
		writeError(c, http.StatusBadRequest, "url is required")
		return
	}
	if examID == "" {
		writeError(c, http.StatusBadRequest, "examId is required")
		return
	}
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang (e.g. de/en/ja)")
		return
	}
	if model == "" {
		model = "small"
	}

	// 生成 job_id
	jobIDBytes := make([]byte, 8)
	if _, err := rand.Read(jobIDBytes); err != nil {
		writeError(c, http.StatusInternalServerError, "generate job id failed")
		return
	}
	jobID := fmt.Sprintf("listen-%s-%s-%s",
		time.Now().Format("20060102-150405"), lang, hex.EncodeToString(jobIDBytes))

	// 上传文件存到 WORK_DIR/uploads/{job_id}/
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	uploadDir := filepath.Join(workRoot, "uploads", jobID)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		h.logger.Printf("mkdir uploads %s: %v", uploadDir, err)
		writeError(c, http.StatusInternalServerError, "prepare upload dir failed")
		return
	}

	var answersPath, cookiesPath string
	if fh, _, err := c.Request.FormFile("answers"); err == nil {
		dst := filepath.Join(uploadDir, "answers.csv")
		if err := saveUploadedFile(fh, dst); err != nil {
			fh.Close()
			writeError(c, http.StatusInternalServerError, "save answers failed: "+err.Error())
			return
		}
		fh.Close()
		answersPath = dst
	}
	if fh, _, err := c.Request.FormFile("cookies"); err == nil {
		dst := filepath.Join(uploadDir, "cookies.txt")
		if err := saveUploadedFile(fh, dst); err != nil {
			fh.Close()
			writeError(c, http.StatusInternalServerError, "save cookies failed: "+err.Error())
			return
		}
		fh.Close()
		cookiesPath = dst
	}

	// 入库 pending
	hasAnswers := answersPath != ""
	if err := h.store.CreateListeningJob(c.Request.Context(), jobID, examID, lang, url, hasAnswers); err != nil {
		h.logger.Printf("create listening job: %v", err)
		writeError(c, http.StatusInternalServerError, "create job failed")
		return
	}

	// 构造 builder.listen args（Runner.cmd = "uv"，RunCustom 直接用 args）
	args := []string{
		"run", "--no-sync", "python", "-m", "builder.listen",
		"--url", url,
		"--exam-id", examID,
		"--lang", lang,
		"--target-lang", lang,
		"--model", model,
	}
	if answersPath != "" {
		args = append(args, "--answers", answersPath)
	}
	if cookiesPath != "" {
		args = append(args, "--cookies", cookiesPath)
	}

	// 触发调度
	err := h.sched.TriggerListening(jobID, examID, lang, args, nil)
	switch {
	case errors.Is(err, scheduler.ErrBusy):
		writeError(c, http.StatusConflict, err.Error())
		return
	case errors.Is(err, scheduler.ErrLowMemory):
		writeError(c, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		h.logger.Printf("trigger listening: %v", err)
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(c, http.StatusAccepted, map[string]any{
		"job_id":  jobID,
		"exam_id": examID,
		"lang":    lang,
		"status":  "pending",
	})
}

func (h *Handler) listeningJobs(c *gin.Context) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if n, err := fmt.Sscanf(raw, "%d", &limit); n == 1 && err == nil && limit > 0 && limit <= 500 {
			// ok
		} else {
			writeError(c, http.StatusBadRequest, "limit must be 1..500")
			return
		}
	}
	jobs, err := h.store.ListListeningJobs(c.Request.Context(), limit)
	if err != nil {
		h.logger.Printf("list listening jobs: %v", err)
		writeError(c, http.StatusInternalServerError, "query jobs failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"jobs": jobs})
}

func (h *Handler) listeningJobDetail(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	afterLogID, _ := strconv.ParseInt(c.Query("after_log_id"), 10, 64)
	job, logs, err := h.store.ListeningJobDetail(c.Request.Context(), jobID, afterLogID)
	if err != nil {
		h.logger.Printf("listening job detail %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(c, http.StatusNotFound, "job not found: "+jobID)
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"job":  job,
		"logs": logs,
	})
}

// listeningFile 是导入任务产物的一个可下载文件。
type listeningFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

var errListeningJobNotFound = errors.New("listening job not found")

// listeningExamDir 定位任务产物目录：WORK_DIR/listening/{exam_id}。
func (h *Handler) listeningExamDir(ctx context.Context, jobID string) (string, string, error) {
	job, _, err := h.store.ListeningJobDetail(ctx, jobID, 0)
	if err != nil {
		return "", "", err
	}
	if job == nil {
		return "", "", errListeningJobNotFound
	}
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	return filepath.Join(workRoot, "listening", job.ExamID), job.ExamID, nil
}

// listeningFiles 列出该任务目录下的全部产物（视频、音频、图片、转写文件等）。
func (h *Handler) listeningFiles(c *gin.Context) {
	dir, examID, err := h.listeningExamDir(c.Request.Context(), c.Param("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	files := make([]listeningFile, 0, 32)

	// 确保 zip 缓存存在（不存在则预生成），这样用户可直接在文件列表里下载 zip，
	// 走 /download 路径，与单文件下载完全一致，避免 /archive 路径被网关限制。
	zipPath := filepath.Join(dir, examID+".zip")
	if _, statErr := os.Stat(zipPath); statErr != nil {
		if err := buildListeningZip(dir, zipPath); err != nil {
			h.logger.Printf("prebuild listening zip failed: %v", err)
		}
	}

	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		// 排除转写中间产物（16k wav，体积大且无下载价值）
		if strings.HasSuffix(path, "source16k.wav") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		files = append(files, listeningFile{
			Path: filepath.ToSlash(rel),
			Name: entry.Name(),
			Size: info.Size(),
		})
		return nil
	})
	if walkErr != nil {
		h.logger.Printf("walk listening dir %s: %v", dir, walkErr)
	}

	writeJSON(c, http.StatusOK, map[string]any{
		"exam_id": examID,
		"files":   files,
	})
}

// listeningDownload 下载单个产物文件，path 必须是任务目录内的相对路径。
func (h *Handler) listeningDownload(c *gin.Context) {
	dir, _, err := h.listeningExamDir(c.Request.Context(), c.Param("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	target, err := safeJoin(dir, c.Query("path"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}

	// 如果请求的是 zip 包，确保它已完整生成（原子 rename，不会读到半成品）。
	// 这样用户无论何时点下载，都能拿到完整的 zip。
	if strings.HasSuffix(target, ".zip") {
		if _, statErr := os.Stat(target); statErr != nil {
			if buildErr := buildListeningZip(dir, target); buildErr != nil {
				h.logger.Printf("build listening zip on download failed: %v", buildErr)
				writeError(c, http.StatusInternalServerError, "build zip failed")
				return
			}
		}
	}

	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		writeError(c, http.StatusNotFound, "file not found")
		return
	}

	c.Header("Content-Type", contentTypeFor(target))
	if isInlinePlayable(target) {
		// 音视频、图片、文本等可在浏览器内联展示，方便管理后台直接试听/预览。
		c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(target)))
	} else {
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(target)))
	}
	c.Header("X-Accel-Buffering", "no")
	http.ServeFile(c.Writer, c.Request, target)
}

// contentTypeFor 根据扩展名返回合适的 MIME type，容器里 mime.types 可能不全，
// 对常见音视频/图片扩展名做显式映射。
func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".txt", ".srt", ".vtt":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	default:
		if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
			return t
		}
		return "application/octet-stream"
	}
}

// isInlinePlayable 判断文件是否适合在浏览器内联展示（试听/预览）。
func isInlinePlayable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a", ".mp3", ".wav", ".ogg", ".oga", ".flac",
		".mp4", ".webm", ".mov",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg",
		".txt", ".srt", ".vtt", ".json":
		return true
	default:
		return false
	}
}

// listeningArchive 把整个任务目录打包成 zip 下载（视频 + 音频 + 图片 + 转写）。
// 策略：首次请求时把 zip 预生成到任务目录下并持久化（不删除），之后直接 ServeFile。
// 这样 zip 下载与单文件下载走完全相同的 ServeFile 路径，避免流式打包导致的下载卡住。
func (h *Handler) listeningArchive(c *gin.Context) {
	dir, examID, err := h.listeningExamDir(c.Request.Context(), c.Param("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		writeError(c, http.StatusNotFound, "exam output not found")
		return
	}

	zipPath := filepath.Join(dir, examID+".zip")

	// 缓存不存在则生成（用 Store 不压缩，速度快）。生成到磁盘文件而非 response，
	// 生成完成后走 ServeFile，与单文件下载路径一致，稳定支持大文件下载。
	if _, statErr := os.Stat(zipPath); statErr != nil {
		if err := buildListeningZip(dir, zipPath); err != nil {
			h.logger.Printf("build listening zip failed: %v", err)
			writeError(c, http.StatusInternalServerError, "build zip failed")
			return
		}
	}

	// 告诉 nginx 等反向代理不要缓冲此响应，否则大文件下载会卡在代理缓冲阶段。
	c.Header("X-Accel-Buffering", "no")
	http.ServeFile(c.Writer, c.Request, zipPath)
}

// buildListeningZip 把 dir 下的文件（排除 source16k.wav）用 Store 模式打包到 zipPath。
// 先写到临时文件，完成后 rename 到目标路径（原子操作），避免下载时读到半成品。
func buildListeningZip(dir, zipPath string) error {
	tmp, err := os.CreateTemp(filepath.Dir(zipPath), ".listening-*.zip.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// 清理临时文件（成功 rename 后不影响，失败时兜底）
	defer os.Remove(tmpPath)

	zw := zip.NewWriter(tmp)
	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		// 排除自身 zip 与转写中间产物
		if path == zipPath || strings.HasSuffix(path, "source16k.wav") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		src, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer src.Close()
		dst, createErr := zw.CreateHeader(&zip.FileHeader{
			Name:   filepath.ToSlash(rel),
			Method: zip.Store,
		})
		if createErr != nil {
			return nil
		}
		_, _ = io.Copy(dst, src)
		return nil
	})
	if closeErr := zw.Close(); closeErr != nil {
		tmp.Close()
		return closeErr
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if walkErr != nil {
		return walkErr
	}
	// 原子替换：rename 保证下载请求要么读到旧的完整文件，要么读到新的完整文件。
	return os.Rename(tmpPath, zipPath)
}

// safeJoin 把相对路径安全地拼到 root 下，拒绝越界与绝对路径。
func safeJoin(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" {
		return "", errors.New("missing path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("invalid path")
	}
	rootClean := filepath.Clean(root)
	full := filepath.Join(rootClean, clean)
	if full != rootClean && !strings.HasPrefix(full, rootClean+string(os.PathSeparator)) {
		return "", errors.New("path escapes exam dir")
	}
	return full, nil
}

func saveUploadedFile(src io.ReadCloser, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, src); err != nil {
		return err
	}
	return nil
}

// listeningJobAnswers 返回任务 manifest 中已绑定的答案，供管理台回显。
func (h *Handler) listeningJobAnswers(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	dir, _, err := h.listeningExamDir(c.Request.Context(), jobID)
	switch {
	case err == errListeningJobNotFound:
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("listening exam dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		writeError(c, http.StatusNotFound, "manifest not found")
		return
	}
	var manifest struct {
		Questions []struct {
			Number int `json:"number"`
			Answer any `json:"answer"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		writeError(c, http.StatusInternalServerError, "manifest parse failed")
		return
	}
	type qBrief struct {
		Number int    `json:"number"`
		Answer string `json:"answer"`
	}
	out := make([]qBrief, 0, len(manifest.Questions))
	for _, q := range manifest.Questions {
		if answer, ok := q.Answer.(string); ok && strings.TrimSpace(answer) != "" {
			out = append(out, qBrief{Number: q.Number, Answer: answer})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	writeJSON(c, http.StatusOK, map[string]any{"answers": out})
}

// listeningUpdateAnswers 允许管理员在任务完成后补充答案 CSV。
// CSV 格式：题号,答案（选择 A/B/C/D；判断 richtig/falsch）。更新 manifest.json 中对应题目的 answer 字段。
// 重新上传视为覆盖：文件中的答案是唯一答案，未出现在文件里的旧答案会被清空。
func (h *Handler) listeningUpdateAnswers(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		writeError(c, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}
	file, _, err := c.Request.FormFile("answers")
	if err != nil {
		writeError(c, http.StatusBadRequest, "missing answers.csv")
		return
	}
	defer file.Close()

	answerMap, err := parseAnswerCSV(file)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	if len(answerMap) == 0 {
		writeError(c, http.StatusBadRequest, "答案文件里没有解析到任何题号")
		return
	}

	dir, _, err := h.listeningExamDir(c.Request.Context(), jobID)
	switch {
	case err == errListeningJobNotFound:
		writeError(c, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("listening exam dir: %v", err)
		writeError(c, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "manifest not found")
		return
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		writeError(c, http.StatusInternalServerError, "manifest parse failed")
		return
	}
	questions, ok := manifest["questions"].([]any)
	if !ok {
		writeError(c, http.StatusInternalServerError, "manifest questions invalid")
		return
	}

	updated := 0
	for _, q := range questions {
		qMap, ok := q.(map[string]any)
		if !ok {
			continue
		}
		// 重新上传视为覆盖：先清空旧答案，再写入本次文件里的答案。
		qMap["answer"] = nil
		num, ok := qMap["number"].(float64)
		if !ok {
			continue
		}
		if ans, exists := answerMap[int(num)]; exists {
			qMap["answer"] = ans
			updated++
		}
	}
	manifest["questions"] = questions

	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		writeError(c, http.StatusInternalServerError, "manifest encode failed")
		return
	}
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		writeError(c, http.StatusInternalServerError, "manifest write failed")
		return
	}

	type qBrief struct {
		Number int    `json:"number"`
		Answer string `json:"answer"`
	}
	updatedList := make([]qBrief, 0, len(answerMap))
	for n := range answerMap {
		updatedList = append(updatedList, qBrief{Number: n, Answer: answerMap[n]})
	}
	sort.Slice(updatedList, func(i, j int) bool { return updatedList[i].Number < updatedList[j].Number })

	// 上传答案后标记该素材已绑定答案
	if err := h.store.SetListeningJobHasAnswers(c.Request.Context(), jobID); err != nil {
		h.logger.Printf("set has_answers for job %s: %v", jobID, err)
	}

	writeJSON(c, http.StatusOK, map[string]any{
		"updated_count": updated,
		"answers":       updatedList,
	})
}

// updateListeningJobMeta 更新听力任务的显示名称与等级；空名称表示恢复使用 Exam ID。
func (h *Handler) updateListeningJobMeta(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	var req struct {
		Name  string `json:"name"`
		Level string `json:"level"`
	}
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	var name *string
	if req.Name != "" {
		name = &req.Name
	}
	if req.Level != "" && !validCefrLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "invalid level (A1/A2/B1/B2/C1/C2)")
		return
	}
	if err := h.store.UpdateListeningJobMeta(c.Request.Context(), jobID, name, req.Level); err != nil {
		h.logger.Printf("update listening job meta %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "update job failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// deleteListeningJob 软删除听力任务（标记 deleted_at，不删除产物文件）。
func (h *Handler) deleteListeningJob(c *gin.Context) {
	jobID := c.Param("jobId")
	if jobID == "" {
		writeError(c, http.StatusBadRequest, "missing jobId")
		return
	}
	if err := h.store.SoftDeleteListeningJob(c.Request.Context(), jobID); err != nil {
		h.logger.Printf("soft delete listening job %s: %v", jobID, err)
		writeError(c, http.StatusInternalServerError, "delete job failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// parseAnswerCSV 解析答案 CSV：题号,答案。
// 答案支持：选择题 A/B/C/D；判断题 richtig/falsch（或缩写 r/f、true/false）。
// 归一化后存储：A/B/C/D（选择）、R（正确）/F（错误）。忽略表头与非法行。
func parseAnswerCSV(r io.Reader) (map[int]string, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV 解析失败：%w", err)
	}
	result := make(map[int]string)
	for _, row := range rows {
		cells := make([]string, 0, len(row))
		for _, c := range row {
			c = strings.TrimSpace(c)
			if c != "" {
				cells = append(cells, c)
			}
		}
		if len(cells) < 2 {
			continue
		}
		numStr := strings.TrimLeft(cells[0], "qQ")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		answer, err := normalizeAnswer(cells[1])
		if err != nil {
			return nil, fmt.Errorf("第 %d 题 %w", num, err)
		}
		result[num] = answer
	}
	return result, nil
}

// normalizeAnswer 将各种答案写法归一化为标准单字符：
// 选择题 → A/B/C/D；判断题 → R（正确）/ F（错误）。
func normalizeAnswer(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "a":
		return "A", nil
	case "b":
		return "B", nil
	case "c":
		return "C", nil
	case "d":
		return "D", nil
	case "r", "richtig", "true", "t":
		return "R", nil
	case "f", "falsch", "false":
		return "F", nil
	default:
		return "", fmt.Errorf("不支持的答案 %q（支持 A/B/C/D 或 richtig/falsch）", raw)
	}
}

// ---------------------------------------------------------------------------
// 关卡配置
// ---------------------------------------------------------------------------

type levelUpsert struct {
	Name        string `json:"name"`
	Lang        string `json:"lang"`
	Difficulty  string `json:"difficulty"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
	Active      bool   `json:"active"`
}

var validDifficulties = map[string]bool{"A1": true, "A2": true, "B1": true, "B2": true}

// validCefrLevels 是听力/口语试题共用的等级枚举，对应数据库类型 cefr_level。
var validCefrLevels = map[string]bool{
	"A1": true, "A2": true, "B1": true, "B2": true, "C1": true, "C2": true,
}

func parseLevelID(c *gin.Context) (int64, error) {
	return strconv.ParseInt(c.Param("id"), 10, 64)
}

// listLevels 返回关卡列表。?active_only=true 时只返回已生效的关卡。
func (h *Handler) listLevels(c *gin.Context) {
	activeOnly := c.Query("active_only") == "true"
	levels, err := h.store.ListLevels(c.Request.Context(), activeOnly)
	if err != nil {
		h.logger.Printf("list levels: %v", err)
		writeError(c, http.StatusInternalServerError, "list levels failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"levels": levels})
}

// listSpeakingItems 返回口语固定问答题库。支持 ?lang=de&level=A1&active_only=false。
func (h *Handler) listSpeakingItems(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	activeOnly := c.Query("active_only") != "false"
	items, err := h.store.ListSpeakingItems(c.Request.Context(), lang, level, activeOnly)
	if err != nil {
		h.logger.Printf("list speaking items: %v", err)
		writeError(c, http.StatusInternalServerError, "list speaking items failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"items": items})
}

// listSpeakingExamParts 返回口语考试的固定部分配置（A1/A2 官方流程的结构来源）。
func (h *Handler) listSpeakingExamParts(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	activeOnly := c.Query("active_only") != "false"
	parts, err := h.store.ListSpeakingExamParts(c.Request.Context(), lang, level, activeOnly)
	if err != nil {
		h.logger.Printf("list speaking exam parts: %v", err)
		writeError(c, http.StatusInternalServerError, "list speaking exam parts failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"parts": parts})
}

// speakingExamPartUpsert 是口语考试部分配置的创建/更新请求体。
type speakingExamPartUpsert struct {
	Lang         string          `json:"lang"`
	Level        string          `json:"level"`
	PartKey      string          `json:"part_key"`
	Title        string          `json:"title"`
	Mode         string          `json:"mode"`
	DurationSec  int             `json:"duration_sec"`
	MaxFollowups int             `json:"max_followups"`
	Role         string          `json:"role"`
	Aid          json.RawMessage `json:"aid"`
	SortOrder    int             `json:"sort_order"`
	Active       bool            `json:"active"`
}

var validExamPartModes = map[string]bool{
	"intro": true, "qa": true, "request": true, "monologue": true,
	"planning": true, "presentation": true, "discussion": true,
}

var validExamAidKinds = map[string]bool{
	"chips": true, "cards": true, "mindmap": true, "schedule": true,
}

// speakingExamPartFromRequest 校验并规范化请求体。
func speakingExamPartFromRequest(req speakingExamPartUpsert) (store.SpeakingExamPart, string) {
	req.Lang = strings.TrimSpace(req.Lang)
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.PartKey = strings.TrimSpace(req.PartKey)
	req.Title = strings.TrimSpace(req.Title)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Role = strings.TrimSpace(req.Role)

	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		return store.SpeakingExamPart{}, "invalid lang"
	}
	if !validCefrLevels[req.Level] {
		return store.SpeakingExamPart{}, "invalid level (A1/A2/B1/B2/C1/C2)"
	}
	if !validExamParts[req.PartKey] {
		return store.SpeakingExamPart{}, "invalid part_key"
	}
	if req.Title == "" {
		return store.SpeakingExamPart{}, "title is required"
	}
	if !validExamPartModes[req.Mode] {
		return store.SpeakingExamPart{}, "invalid mode"
	}
	if req.DurationSec < 30 || req.DurationSec > 3600 {
		return store.SpeakingExamPart{}, "duration_sec 需在 30–3600 秒之间"
	}
	if req.MaxFollowups < 0 || req.MaxFollowups > 10 {
		return store.SpeakingExamPart{}, "max_followups 需在 0–10 之间"
	}
	if req.Role == "" {
		req.Role = "examiner"
	}
	if req.Role != "examiner" && req.Role != "partner" {
		return store.SpeakingExamPart{}, "role 只能是 examiner 或 partner"
	}

	var aid json.RawMessage
	if len(req.Aid) > 0 && string(req.Aid) != "null" {
		var probe struct {
			Kind  string `json:"kind"`
			Label string `json:"label"`
		}
		if err := json.Unmarshal(req.Aid, &probe); err != nil {
			return store.SpeakingExamPart{}, "aid 必须是 JSON 对象"
		}
		if !validExamAidKinds[probe.Kind] {
			return store.SpeakingExamPart{}, "aid.kind 只能是 chips/cards/mindmap/schedule"
		}
		if strings.TrimSpace(probe.Label) == "" {
			return store.SpeakingExamPart{}, "aid.label is required"
		}
		aid = req.Aid
	}

	return store.SpeakingExamPart{
		Lang:         req.Lang,
		Level:        req.Level,
		PartKey:      req.PartKey,
		Title:        req.Title,
		Mode:         req.Mode,
		DurationSec:  req.DurationSec,
		MaxFollowups: req.MaxFollowups,
		Role:         req.Role,
		Aid:          aid,
		SortOrder:    req.SortOrder,
		Active:       req.Active,
	}, ""
}

// createSpeakingExamPart 新增口语考试部分配置。
func (h *Handler) createSpeakingExamPart(c *gin.Context) {
	var req speakingExamPartUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	part, msg := speakingExamPartFromRequest(req)
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	existing, err := h.store.ListSpeakingExamParts(c.Request.Context(), part.Lang, part.Level, false)
	if err != nil {
		h.logger.Printf("list speaking exam parts: %v", err)
		writeError(c, http.StatusInternalServerError, "create speaking exam part failed")
		return
	}
	for _, item := range existing {
		if item.PartKey == part.PartKey {
			writeError(c, http.StatusConflict, "该等级下已存在同一部分配置")
			return
		}
	}
	id, err := h.store.CreateSpeakingExamPart(c.Request.Context(), part)
	if err != nil {
		h.logger.Printf("create speaking exam part: %v", err)
		writeError(c, http.StatusInternalServerError, "create speaking exam part failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"id": id})
}

// updateSpeakingExamPart 更新口语考试部分配置。
func (h *Handler) updateSpeakingExamPart(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	var req speakingExamPartUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	part, msg := speakingExamPartFromRequest(req)
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	part.ID = id

	existing, err := h.store.ListSpeakingExamParts(c.Request.Context(), part.Lang, part.Level, false)
	if err != nil {
		h.logger.Printf("list speaking exam parts: %v", err)
		writeError(c, http.StatusInternalServerError, "update speaking exam part failed")
		return
	}
	for _, item := range existing {
		if item.PartKey == part.PartKey && item.ID != id {
			writeError(c, http.StatusConflict, "该等级下已存在同一部分配置")
			return
		}
	}

	if err := h.store.UpdateSpeakingExamPart(c.Request.Context(), part); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking exam part not found")
			return
		}
		h.logger.Printf("update speaking exam part: %v", err)
		writeError(c, http.StatusInternalServerError, "update speaking exam part failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// deleteSpeakingExamPart 删除口语考试部分配置。
func (h *Handler) deleteSpeakingExamPart(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteSpeakingExamPart(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking exam part not found")
			return
		}
		h.logger.Printf("delete speaking exam part: %v", err)
		writeError(c, http.StatusInternalServerError, "delete speaking exam part failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// speakingItemUpsert 是口语题目的创建/更新请求体。
type speakingItemUpsert struct {
	ItemKey        string `json:"item_key"`
	Lang           string `json:"lang"`
	Level          string `json:"level"`
	ExamPart       string `json:"exam_part"`
	Topic          string `json:"topic"`
	Role           string `json:"role"`
	PromptDE       string `json:"prompt_de"`
	PromptZH       string `json:"prompt_zh"`
	ExpectedPoints string `json:"expected_points"`
	RefAnswer      string `json:"ref_answer"`
	ErrorNote      string `json:"error_note"`
	SortOrder      int    `json:"sort_order"`
	Active         bool   `json:"active"`
}

var validExamParts = map[string]bool{
	"part1_intro":        true,
	"part2_presentation": true,
	"part3_discussion":   true,
}

// speakingItemFromRequest 校验并规范化请求体。
func speakingItemFromRequest(req speakingItemUpsert) (store.SpeakingItem, string) {
	req.ItemKey = strings.TrimSpace(req.ItemKey)
	req.Lang = strings.TrimSpace(req.Lang)
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.ExamPart = strings.TrimSpace(req.ExamPart)
	req.Topic = strings.TrimSpace(req.Topic)
	req.Role = strings.TrimSpace(req.Role)
	req.PromptDE = strings.TrimSpace(req.PromptDE)
	req.PromptZH = strings.TrimSpace(req.PromptZH)
	req.ExpectedPoints = strings.TrimSpace(req.ExpectedPoints)
	req.RefAnswer = strings.TrimSpace(req.RefAnswer)
	req.ErrorNote = strings.TrimSpace(req.ErrorNote)

	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		return store.SpeakingItem{}, "invalid lang"
	}
	if !validCefrLevels[req.Level] {
		return store.SpeakingItem{}, "invalid level (A1/A2/B1/B2/C1/C2)"
	}
	if !validExamParts[req.ExamPart] {
		return store.SpeakingItem{}, "invalid exam_part"
	}
	if req.Topic == "" {
		return store.SpeakingItem{}, "topic is required"
	}
	if req.PromptDE == "" {
		return store.SpeakingItem{}, "prompt_de is required"
	}
	if req.Role == "" {
		req.Role = "examiner"
	}
	if req.ExpectedPoints != "" {
		var points []string
		if err := json.Unmarshal([]byte(req.ExpectedPoints), &points); err != nil {
			return store.SpeakingItem{}, "expected_points 必须是 JSON 字符串数组"
		}
	}

	return store.SpeakingItem{
		ItemKey:        req.ItemKey,
		Lang:           req.Lang,
		Level:          req.Level,
		ExamPart:       req.ExamPart,
		Topic:          req.Topic,
		Role:           req.Role,
		PromptDE:       req.PromptDE,
		PromptZH:       req.PromptZH,
		ExpectedPoints: req.ExpectedPoints,
		RefAnswer:      req.RefAnswer,
		ErrorNote:      req.ErrorNote,
		SortOrder:      req.SortOrder,
		Active:         req.Active,
	}, ""
}

// getSpeakingItem 返回单条口语题目。
func (h *Handler) getSpeakingItem(c *gin.Context) {
	item, err := h.store.GetSpeakingItem(c.Request.Context(), c.Param("key"))
	if err != nil {
		h.logger.Printf("get speaking item: %v", err)
		writeError(c, http.StatusInternalServerError, "get speaking item failed")
		return
	}
	if item == nil {
		writeError(c, http.StatusNotFound, "speaking item not found")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"item": item})
}

// createSpeakingItem 新增口语题目；item_key 为空时自动生成。
func (h *Handler) createSpeakingItem(c *gin.Context) {
	var req speakingItemUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	item, msg := speakingItemFromRequest(req)
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	if item.ItemKey == "" {
		item.ItemKey = fmt.Sprintf("%s-%s-%d", item.Lang, strings.ToLower(item.Level), time.Now().UnixNano())
	}
	if err := h.store.CreateSpeakingItem(c.Request.Context(), item); err != nil {
		h.logger.Printf("create speaking item: %v", err)
		writeError(c, http.StatusInternalServerError, "create speaking item failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"item_key": item.ItemKey})
	h.invalidateTopicPlanCache()
}

// updateSpeakingItem 更新口语题目。
func (h *Handler) updateSpeakingItem(c *gin.Context) {
	key := c.Param("key")
	var req speakingItemUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.ItemKey = key
	item, msg := speakingItemFromRequest(req)
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	if err := h.store.UpdateSpeakingItem(c.Request.Context(), item); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking item not found")
			return
		}
		h.logger.Printf("update speaking item: %v", err)
		writeError(c, http.StatusInternalServerError, "update speaking item failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
	h.invalidateTopicPlanCache()
}

// deleteSpeakingItem 删除口语题目。
func (h *Handler) deleteSpeakingItem(c *gin.Context) {
	if err := h.store.DeleteSpeakingItem(c.Request.Context(), c.Param("key")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking item not found")
			return
		}
		h.logger.Printf("delete speaking item: %v", err)
		writeError(c, http.StatusInternalServerError, "delete speaking item failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
	h.invalidateTopicPlanCache()
}

// createLevel 创建关卡。
func (h *Handler) createLevel(c *gin.Context) {
	var req levelUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Lang = strings.TrimSpace(req.Lang)
	req.Difficulty = strings.ToUpper(strings.TrimSpace(req.Difficulty))
	if req.Name == "" {
		writeError(c, http.StatusBadRequest, "name is required")
		return
	}
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	if req.Difficulty == "" {
		req.Difficulty = "A1"
	}
	if !validDifficulties[req.Difficulty] {
		writeError(c, http.StatusBadRequest, "invalid difficulty (A1/A2/B1/B2)")
		return
	}
	id, err := h.store.CreateLevel(c.Request.Context(), req.Name, req.Lang, req.Difficulty, req.Description, req.SortOrder)
	if err != nil {
		h.logger.Printf("create level: %v", err)
		writeError(c, http.StatusInternalServerError, "create level failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"id": id})
}

// getLevel 返回关卡详情（含关联的 exam 列表）。
func (h *Handler) getLevel(c *gin.Context) {
// speakingPlanUpsert 是口语方案的创建/更新请求体。
type speakingPlanUpsert struct {
	Lang        string   `json:"lang"`
	Level       string   `json:"level"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Active      *bool    `json:"active"`
	SortOrder   int      `json:"sort_order"`
	ItemKeys    []string `json:"item_keys"`
}

// speakingPlanFromRequest 校验并规范化方案请求体。
func speakingPlanFromRequest(req speakingPlanUpsert) (store.SpeakingPlan, []string, string) {
	req.Lang = strings.TrimSpace(req.Lang)
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.Name = strings.TrimSpace(req.Name)
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		return store.SpeakingPlan{}, nil, "invalid lang"
	}
	if !validCefrLevels[req.Level] {
		return store.SpeakingPlan{}, nil, "invalid level (A1/A2/B1/B2/C1/C2)"
	}
	if req.Name == "" {
		return store.SpeakingPlan{}, nil, "name is required"
	}
	if len([]rune(req.Name)) > 100 {
		return store.SpeakingPlan{}, nil, "name is too long (max 100)"
	}

	keys := make([]string, 0, len(req.ItemKeys))
	seen := make(map[string]bool, len(req.ItemKeys))
	for _, raw := range req.ItemKeys {
		key := strings.TrimSpace(raw)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return store.SpeakingPlan{}, nil, "item_keys is required"
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}
	return store.SpeakingPlan{
		Lang:        req.Lang,
		Level:       req.Level,
		Name:        req.Name,
		Description: strings.TrimSpace(req.Description),
		Active:      active,
		SortOrder:   req.SortOrder,
	}, keys, ""
}

// validatePlanItems 确认方案题目都属于该难度且存在。
func (h *Handler) validatePlanItems(c *gin.Context, level string, keys []string) string {
	count, err := h.store.CountSoloSpeakingItems(c.Request.Context(), level, keys)
	if err != nil {
		h.logger.Printf("count speaking items: %v", err)
		return "validate speaking items failed"
	}
	if count != int64(len(keys)) {
		return "存在不属于该难度或已删除的题目"
	}
	return ""
}

// listSpeakingPlans 返回口语方案列表。
func (h *Handler) listSpeakingPlans(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	activeOnly := c.Query("active_only") != "false"
	plans, err := h.store.ListSpeakingPlans(c.Request.Context(), lang, level, activeOnly)
	if err != nil {
		h.logger.Printf("list speaking plans: %v", err)
		writeError(c, http.StatusInternalServerError, "list speaking plans failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"plans": plans})
}

// getSpeakingPlan 返回方案及其题目。
func (h *Handler) getSpeakingPlan(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	plan, items, err := h.store.GetSpeakingPlan(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking plan not found")
			return
		}
		h.logger.Printf("get speaking plan: %v", err)
		writeError(c, http.StatusInternalServerError, "get speaking plan failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"plan": plan, "items": items})
}

// createSpeakingPlan 新建口语方案。
func (h *Handler) createSpeakingPlan(c *gin.Context) {
	var req speakingPlanUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	plan, keys, msg := speakingPlanFromRequest(req)
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	if msg := h.validatePlanItems(c, plan.Level, keys); msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	existing, err := h.store.ListSpeakingPlans(c.Request.Context(), plan.Lang, plan.Level, false)
	if err != nil {
		h.logger.Printf("list speaking plans: %v", err)
		writeError(c, http.StatusInternalServerError, "create speaking plan failed")
		return
	}
	for _, item := range existing {
		if item.Name == plan.Name {
			writeError(c, http.StatusConflict, "该难度下已存在同名方案")
			return
		}
	}
	id, err := h.store.CreateSpeakingPlan(c.Request.Context(), plan, keys)
	if err != nil {
		h.logger.Printf("create speaking plan: %v", err)
		writeError(c, http.StatusInternalServerError, "create speaking plan failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"id": id})
}

// updateSpeakingPlan 更新口语方案。
func (h *Handler) updateSpeakingPlan(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	var req speakingPlanUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	plan, keys, msg := speakingPlanFromRequest(req)
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	if msg := h.validatePlanItems(c, plan.Level, keys); msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	existing, err := h.store.ListSpeakingPlans(c.Request.Context(), plan.Lang, plan.Level, false)
	if err != nil {
		h.logger.Printf("list speaking plans: %v", err)
		writeError(c, http.StatusInternalServerError, "update speaking plan failed")
		return
	}
	for _, item := range existing {
		if item.Name == plan.Name && item.ID != id {
			writeError(c, http.StatusConflict, "该难度下已存在同名方案")
			return
		}
	}
	if err := h.store.UpdateSpeakingPlan(c.Request.Context(), id, plan, keys); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking plan not found")
			return
		}
		h.logger.Printf("update speaking plan: %v", err)
		writeError(c, http.StatusInternalServerError, "update speaking plan failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// deleteSpeakingPlan 删除口语方案。
func (h *Handler) deleteSpeakingPlan(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteSpeakingPlan(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking plan not found")
			return
		}
		h.logger.Printf("delete speaking plan: %v", err)
		writeError(c, http.StatusInternalServerError, "delete speaking plan failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

	id, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	lv, err := h.store.GetLevel(c.Request.Context(), id)
	if err != nil {
		h.logger.Printf("get level: %v", err)
		writeError(c, http.StatusInternalServerError, "get level failed")
		return
	}
	if lv == nil {
		writeError(c, http.StatusNotFound, "level not found")
		return
	}
	exams, err := h.store.ListLevelExams(c.Request.Context(), id)
	if err != nil {
		h.logger.Printf("list level exams: %v", err)
		writeError(c, http.StatusInternalServerError, "list level exams failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"level": lv, "exams": exams})
}

// updateLevel 更新关卡。
func (h *Handler) updateLevel(c *gin.Context) {
	id, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	var req levelUpsert
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Difficulty = strings.ToUpper(strings.TrimSpace(req.Difficulty))
	if req.Name == "" {
		writeError(c, http.StatusBadRequest, "name is required")
		return
	}
	if req.Difficulty == "" {
		req.Difficulty = "A1"
	}
	if !validDifficulties[req.Difficulty] {
		writeError(c, http.StatusBadRequest, "invalid difficulty (A1/A2/B1/B2)")
		return
	}
	if err := h.store.UpdateLevel(c.Request.Context(), id, req.Name, req.Difficulty, req.Description, req.SortOrder, req.Active); err != nil {
		h.logger.Printf("update level: %v", err)
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// deleteLevel 删除关卡。
func (h *Handler) deleteLevel(c *gin.Context) {
	id, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	if err := h.store.DeleteLevel(c.Request.Context(), id); err != nil {
		h.logger.Printf("delete level: %v", err)
		writeError(c, http.StatusInternalServerError, "delete level failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// listLevelExams 返回关卡关联的 exam 列表。
func (h *Handler) listLevelExams(c *gin.Context) {
	id, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	exams, err := h.store.ListLevelExams(c.Request.Context(), id)
	if err != nil {
		h.logger.Printf("list level exams: %v", err)
		writeError(c, http.StatusInternalServerError, "list level exams failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"exams": exams})
}

type addExamReq struct {
	ExamID    string `json:"exam_id"`
	ItemType  string `json:"item_type"`
	SortOrder int    `json:"sort_order"`
}

var validItemTypes = map[string]bool{
	"listening": true, "speaking": true, "speaking_topic": true, "speaking_plan": true,
}

// addLevelExam 给关卡添加一个题目（听力或口语）。
func (h *Handler) addLevelExam(c *gin.Context) {
	id, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	var req addExamReq
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.ExamID = strings.TrimSpace(req.ExamID)
	req.ItemType = strings.ToLower(strings.TrimSpace(req.ItemType))
	if req.ExamID == "" {
		writeError(c, http.StatusBadRequest, "exam_id is required")
		return
	}
	if req.ItemType == "" {
		req.ItemType = "listening"
	}
	if !validItemTypes[req.ItemType] {
		writeError(c, http.StatusBadRequest, "invalid item_type (listening/speaking_plan)")
		return
	}
	if err := h.store.AddLevelExam(c.Request.Context(), id, req.ItemType, req.ExamID, req.SortOrder); err != nil {
		h.logger.Printf("add level exam: %v", err)
		writeError(c, http.StatusInternalServerError, "add level exam failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// removeLevelExam 从关卡移除一个题目。?item_type=listening|speaking，默认 listening。
func (h *Handler) removeLevelExam(c *gin.Context) {
	id, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	examID := c.Param("examId")
	if examID == "" {
		writeError(c, http.StatusBadRequest, "examId is required")
		return
	}
	itemType := strings.ToLower(strings.TrimSpace(c.Query("item_type")))
	if itemType == "" {
		itemType = "listening"
	}
	if !validItemTypes[itemType] {
		writeError(c, http.StatusBadRequest, "invalid item_type (listening/speaking_plan)")
		return
	}
	if err := h.store.RemoveLevelExam(c.Request.Context(), id, itemType, examID); err != nil {
		h.logger.Printf("remove level exam: %v", err)
		writeError(c, http.StatusInternalServerError, "remove level exam failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// listAvailableExams 返回可加入关卡的听力与口语题目。
func (h *Handler) listAvailableExams(c *gin.Context) {
	id, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	exams, err := h.store.ListAvailableExams(c.Request.Context(), id)
	if err != nil {
		h.logger.Printf("list available exams: %v", err)
		writeError(c, http.StatusInternalServerError, "list available exams failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"exams": exams})
}
