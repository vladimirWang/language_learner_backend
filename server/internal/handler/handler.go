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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	mux.HandleFunc("GET /api/listening/job/{jobId}/files", h.listeningFiles)
	mux.HandleFunc("GET /api/listening/job/{jobId}/download", h.listeningDownload)
	mux.HandleFunc("GET /api/listening/job/{jobId}/archive", h.listeningArchive)
	mux.HandleFunc("POST /api/listening/job/{jobId}/answers", h.listeningUpdateAnswers)
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

// listeningFile 是导入任务产物的一个可下载文件。
type listeningFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

var errListeningJobNotFound = errors.New("listening job not found")

// listeningExamDir 定位任务产物目录：WORK_DIR/listening/{exam_id}。
func (h *Handler) listeningExamDir(ctx context.Context, jobID string) (string, string, error) {
	job, _, err := h.store.ListeningJobDetail(ctx, jobID)
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
func (h *Handler) listeningFiles(w http.ResponseWriter, r *http.Request) {
	dir, examID, err := h.listeningExamDir(r.Context(), r.PathValue("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(w, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(w, http.StatusInternalServerError, "locate exam dir failed")
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

	writeJSON(w, http.StatusOK, map[string]any{
		"exam_id": examID,
		"files":   files,
	})
}

// listeningDownload 下载单个产物文件，path 必须是任务目录内的相对路径。
func (h *Handler) listeningDownload(w http.ResponseWriter, r *http.Request) {
	dir, _, err := h.listeningExamDir(r.Context(), r.PathValue("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(w, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(w, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	target, err := safeJoin(dir, r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 如果请求的是 zip 包，确保它已完整生成（原子 rename，不会读到半成品）。
	// 这样用户无论何时点下载，都能拿到完整的 zip。
	if strings.HasSuffix(target, ".zip") {
		if _, statErr := os.Stat(target); statErr != nil {
			if buildErr := buildListeningZip(dir, target); buildErr != nil {
				h.logger.Printf("build listening zip on download failed: %v", buildErr)
				writeError(w, http.StatusInternalServerError, "build zip failed")
				return
			}
		}
	}

	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(target)))
	w.Header().Set("X-Accel-Buffering", "no")
	http.ServeFile(w, r, target)
}

// listeningArchive 把整个任务目录打包成 zip 下载（视频 + 音频 + 图片 + 转写）。
// 策略：首次请求时把 zip 预生成到任务目录下并持久化（不删除），之后直接 ServeFile。
// 这样 zip 下载与单文件下载走完全相同的 ServeFile 路径，避免流式打包导致的下载卡住。
func (h *Handler) listeningArchive(w http.ResponseWriter, r *http.Request) {
	dir, examID, err := h.listeningExamDir(r.Context(), r.PathValue("jobId"))
	switch {
	case errors.Is(err, errListeningJobNotFound):
		writeError(w, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("locate listening dir: %v", err)
		writeError(w, http.StatusInternalServerError, "locate exam dir failed")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		writeError(w, http.StatusNotFound, "exam output not found")
		return
	}

	zipPath := filepath.Join(dir, examID+".zip")

	// 缓存不存在则生成（用 Store 不压缩，速度快）。生成到磁盘文件而非 response，
	// 生成完成后走 ServeFile，与单文件下载路径一致，稳定支持大文件下载。
	if _, statErr := os.Stat(zipPath); statErr != nil {
		if err := buildListeningZip(dir, zipPath); err != nil {
			h.logger.Printf("build listening zip failed: %v", err)
			writeError(w, http.StatusInternalServerError, "build zip failed")
			return
		}
	}

	// 告诉 nginx 等反向代理不要缓冲此响应，否则大文件下载会卡在代理缓冲阶段。
	w.Header().Set("X-Accel-Buffering", "no")
	http.ServeFile(w, r, zipPath)
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

// listeningUpdateAnswers 允许管理员在任务完成后补充答案 CSV。
// CSV 格式：题号,答案（答案为 A/B/C/D）。更新 manifest.json 中对应题目的 answer 字段。
func (h *Handler) listeningUpdateAnswers(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "missing jobId")
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}
	file, _, err := r.FormFile("answers")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing answers.csv")
		return
	}
	defer file.Close()

	answerMap, err := parseAnswerCSV(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(answerMap) == 0 {
		writeError(w, http.StatusBadRequest, "答案文件里没有解析到任何题号")
		return
	}

	dir, _, err := h.listeningExamDir(r.Context(), jobID)
	switch {
	case err == errListeningJobNotFound:
		writeError(w, http.StatusNotFound, "job not found")
		return
	case err != nil:
		h.logger.Printf("listening exam dir: %v", err)
		writeError(w, http.StatusInternalServerError, "locate exam dir failed")
		return
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "manifest not found")
		return
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		writeError(w, http.StatusInternalServerError, "manifest parse failed")
		return
	}
	questions, ok := manifest["questions"].([]any)
	if !ok {
		writeError(w, http.StatusInternalServerError, "manifest questions invalid")
		return
	}

	updated := 0
	for _, q := range questions {
		qMap, ok := q.(map[string]any)
		if !ok {
			continue
		}
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
		writeError(w, http.StatusInternalServerError, "manifest encode failed")
		return
	}
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "manifest write failed")
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

	writeJSON(w, http.StatusOK, map[string]any{
		"updated_count": updated,
		"answers":       updatedList,
	})
}

// parseAnswerCSV 解析答案 CSV：题号,答案（A/B/C/D），忽略表头与非法行。
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
		answer := strings.ToUpper(cells[1])
		if answer != "A" && answer != "B" && answer != "C" && answer != "D" {
			return nil, fmt.Errorf("第 %d 题答案不是 A/B/C/D：%s", num, cells[1])
		}
		result[num] = answer
	}
	return result, nil
}
