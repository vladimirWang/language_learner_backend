package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/repo"
	"github.com/wangfernando/language-learner/server/internal/scheduler"
)

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
