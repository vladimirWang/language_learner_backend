// Package handler 暴露版本清单、构建触发与构建状态 HTTP 接口。
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
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
	mux.HandleFunc("POST /api/build/trigger", h.trigger)
	mux.HandleFunc("GET /api/build/status", h.buildStatus)
	mux.HandleFunc("GET /api/repos", h.repos)
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
