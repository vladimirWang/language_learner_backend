// Package handler 暴露版本清单、构建触发与构建状态 HTTP 接口。
package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	logger    *log.Logger
}

// New 创建 HTTP 处理器。
func New(st *store.Store, sch *scheduler.Scheduler, ps *storage.Presigner, repoURLs map[string]string, logger *log.Logger) *Handler {
	return &Handler{store: st, sched: sch, presigner: ps, repoURLs: repoURLs, logger: logger}
}

// Routes 注册全部 API 路由。
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/version", h.version)
	mux.HandleFunc("GET /api/material/{lang}/{file}", h.material)
	mux.HandleFunc("POST /api/build/trigger", h.trigger)
	mux.HandleFunc("GET /api/build/status", h.buildStatus)
	mux.HandleFunc("GET /api/repos", h.repos)
	mux.HandleFunc("POST /api/listening/import", h.listeningImport)
	mux.HandleFunc("GET /api/listening/jobs", h.listeningJobs)
	mux.HandleFunc("GET /api/listening/job/{jobId}", h.listeningJobDetail)
	return h.logRequests(mux)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"status":       "ok",
		"available_mb": h.sched.AvailableMB(),
		"build_busy":   h.sched.Busy(),
		"postgres":     "ok",
	}
	if err := h.store.Ping(r.Context()); err != nil {
		resp["status"] = "degraded"
		resp["postgres"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	lang := r.URL.Query().Get("lang")
	if !langPattern.MatchString(lang) {
		writeError(w, http.StatusBadRequest, "invalid or missing lang")
		return
	}

	v, err := h.store.LatestVersion(r.Context(), lang)
	if err != nil {
		h.logger.Printf("query version lang=%s: %v", lang, err)
		writeError(w, http.StatusInternalServerError, "query version failed")
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "no built version for lang "+lang)
		return
	}

	dbURL, vecURL, err := h.presign(r, v.DBURL, v.VectorURL)
	if err != nil {
		h.logger.Printf("presign version=%s: %v", v.Version, err)
		writeError(w, http.StatusInternalServerError, "sign download url failed")
		return
	}
	// 未配置对象存储时，构建产物落在服务端本地目录：改由本服务直接提供下载。
	if strings.HasPrefix(dbURL, "/") {
		dbURL = "/api/material/" + lang + "/ground_truth.db"
	}
	if strings.HasPrefix(vecURL, "/") {
		vecURL = "/api/material/" + lang + "/vectors.jsonl"
	}
	writeJSON(w, http.StatusOK, map[string]any{
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

func (h *Handler) trigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lang    string `json:"lang"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !langPattern.MatchString(req.Lang) {
		writeError(w, http.StatusBadRequest, "invalid or missing lang")
		return
	}

	version, err := h.sched.Trigger(req.Lang, req.Version)
	switch {
	case errors.Is(err, scheduler.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, scheduler.ErrLowMemory):
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		h.logger.Printf("trigger build: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"lang":    req.Lang,
		"version": version,
		"status":  "building",
	})
}

func (h *Handler) buildStatus(w http.ResponseWriter, r *http.Request) {
	version := r.URL.Query().Get("version")
	if version == "" {
		writeError(w, http.StatusBadRequest, "missing version")
		return
	}
	v, logs, err := h.store.BuildStatus(r.Context(), version)
	if err != nil {
		h.logger.Printf("query build status %s: %v", version, err)
		writeError(w, http.StatusInternalServerError, "query build status failed")
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "version not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
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

func (h *Handler) material(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	name := r.PathValue("file")
	if !langPattern.MatchString(lang) {
		writeError(w, http.StatusBadRequest, "invalid or missing lang")
		return
	}
	if name != "ground_truth.db" && name != "vectors.jsonl" {
		writeError(w, http.StatusNotFound, "unknown material file")
		return
	}

	v, err := h.store.LatestVersion(r.Context(), lang)
	if err != nil {
		h.logger.Printf("query version lang=%s: %v", lang, err)
		writeError(w, http.StatusInternalServerError, "query version failed")
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "no built version for lang "+lang)
		return
	}

	path := v.DBURL
	if name == "vectors.jsonl" {
		path = v.VectorURL
	}
	// 只有落在本机构建目录的产物才能直出；对象存储 key 交给预签名 URL。
	if !filepath.IsAbs(path) || strings.Contains(path, "..") ||
		!strings.HasSuffix(filepath.Clean(path), "/"+lang+"/"+name) {
		writeError(w, http.StatusConflict, "material is not served as a local file")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeFile(w, r, path)
}

func (h *Handler) presign(r *http.Request, dbKey, vecKey string) (string, string, error) {
	dbURL, err := h.presigner.URL(r.Context(), dbKey)
	if err != nil {
		return "", "", err
	}
	vecURL, err := h.presigner.URL(r.Context(), vecKey)
	if err != nil {
		return "", "", err
	}
	return dbURL, vecURL, nil
}

func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		h.logger.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// repos 返回各语种素材仓库的远程状态，以及与最近一次构建的对比。
func (h *Handler) repos(w http.ResponseWriter, r *http.Request) {
	result := make([]map[string]any, 0, len(h.repoURLs))
	for lang, url := range h.repoURLs {
		item := map[string]any{
			"lang":     lang,
			"repo_url": url,
		}

		// 查询远程最新 commit
		commit, err := repo.LatestCommit(r.Context(), url)
		if err != nil {
			item["error"] = err.Error()
			item["latest_commit"] = ""
		} else {
			item["latest_commit"] = commit
			item["latest_commit_short"] = repo.ShortHash(commit)
		}

		// 查询最近一次构建的 source_commit
		lastVersion, err := h.store.LatestVersion(r.Context(), lang)
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
	writeJSON(w, http.StatusOK, map[string]any{"repos": result})
}

// listeningImport 接收音视频 URL + 可选的 answers/cookies 文件，异步启动 builder.listen。
// multipart 字段：url(examId/lang/model) 文本 + answers/cookies 文件（可选）。
func (h *Handler) listeningImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}

	url := strings.TrimSpace(r.FormValue("url"))
	examID := strings.TrimSpace(r.FormValue("examId"))
	lang := strings.TrimSpace(r.FormValue("lang"))
	model := strings.TrimSpace(r.FormValue("model"))

	if url == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if examID == "" {
		writeError(w, http.StatusBadRequest, "examId is required")
		return
	}
	if !langPattern.MatchString(lang) {
		writeError(w, http.StatusBadRequest, "invalid or missing lang (e.g. de/en/ja)")
		return
	}
	if model == "" {
		model = "small"
	}

	// 生成 job_id
	jobIDBytes := make([]byte, 8)
	if _, err := rand.Read(jobIDBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "generate job id failed")
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
		writeError(w, http.StatusInternalServerError, "prepare upload dir failed")
		return
	}

	var answersPath, cookiesPath string
	if fh, _, err := r.FormFile("answers"); err == nil {
		dst := filepath.Join(uploadDir, "answers.csv")
		if err := saveUploadedFile(fh, dst); err != nil {
			fh.Close()
			writeError(w, http.StatusInternalServerError, "save answers failed: "+err.Error())
			return
		}
		fh.Close()
		answersPath = dst
	}
	if fh, _, err := r.FormFile("cookies"); err == nil {
		dst := filepath.Join(uploadDir, "cookies.txt")
		if err := saveUploadedFile(fh, dst); err != nil {
			fh.Close()
			writeError(w, http.StatusInternalServerError, "save cookies failed: "+err.Error())
			return
		}
		fh.Close()
		cookiesPath = dst
	}

	// 入库 pending
	if err := h.store.CreateListeningJob(r.Context(), jobID, examID, lang, url); err != nil {
		h.logger.Printf("create listening job: %v", err)
		writeError(w, http.StatusInternalServerError, "create job failed")
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
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, scheduler.ErrLowMemory):
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		h.logger.Printf("trigger listening: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":  jobID,
		"exam_id": examID,
		"lang":    lang,
		"status":  "pending",
	})
}

func (h *Handler) listeningJobs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := fmt.Sscanf(raw, "%d", &limit); n == 1 && err == nil && limit > 0 && limit <= 500 {
			// ok
		} else {
			writeError(w, http.StatusBadRequest, "limit must be 1..500")
			return
		}
	}
	jobs, err := h.store.ListListeningJobs(r.Context(), limit)
	if err != nil {
		h.logger.Printf("list listening jobs: %v", err)
		writeError(w, http.StatusInternalServerError, "query jobs failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (h *Handler) listeningJobDetail(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "missing jobId")
		return
	}
	job, logs, err := h.store.ListeningJobDetail(r.Context(), jobID)
	if err != nil {
		h.logger.Printf("listening job detail %s: %v", jobID, err)
		writeError(w, http.StatusInternalServerError, "query job failed")
		return
	}
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found: "+jobID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job":  job,
		"logs": logs,
	})
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
