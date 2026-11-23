package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/challenge"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// adminChallengeItem 是管理台预览用的题目（含标准答案）。
type adminChallengeItem struct {
	Seq         int             `json:"seq"`
	Kind        string          `json:"kind"`
	RefID       string          `json:"ref_id"`
	Prompt      json.RawMessage `json:"prompt"`
	Answer      json.RawMessage `json:"answer"`
	Explanation string          `json:"explanation"`
}

// adminListChallenges 返回某月关卡列表（含草稿）。
func (h *Handler) adminListChallenges(c *gin.Context) {
	from, to, month, ok := h.parseChallengeMonth(c)
	if !ok {
		return
	}
	lang := strings.TrimSpace(c.Query("lang"))
	if lang != "" && !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level != "" && !challengeLevelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	list, err := h.store.ListChallengesForMonth(c.Request.Context(), lang, level, from, to)
	if err != nil {
		h.logger.Printf("admin list challenges: %v", err)
		writeError(c, http.StatusInternalServerError, "list challenges failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"month": month, "challenges": list})
}

// adminGetChallenge 返回某天关卡详情，包含标准答案供管理台预览。
func (h *Handler) adminGetChallenge(c *gin.Context) {
	lang, level, day, ok := h.parseAdminChallengeParams(c)
	if !ok {
		return
	}
	ch, err := h.store.GetChallengeByDay(c.Request.Context(), lang, level, day)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("admin get challenge: %v", err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	items, err := h.store.ListChallengeItems(c.Request.Context(), ch.ID)
	if err != nil {
		h.logger.Printf("admin get challenge items: %v", err)
		writeError(c, http.StatusInternalServerError, "load challenge items failed")
		return
	}
	views := make([]adminChallengeItem, 0, len(items))
	for _, item := range items {
		views = append(views, adminChallengeItem{
			Seq: item.Seq, Kind: item.Kind, RefID: item.RefID,
			Prompt: item.Prompt, Answer: item.Answer, Explanation: item.Explanation,
		})
	}
	writeJSON(c, http.StatusOK, map[string]any{"challenge": ch, "items": views})
}

// adminRegenerateChallenge 重新生成关卡内容；已发布关卡不允许修改。
func (h *Handler) adminRegenerateChallenge(c *gin.Context) {
	lang, level, day, ok := h.parseAdminChallengeParams(c)
	if !ok {
		return
	}
	dayTime, _ := time.ParseInLocation("2006-01-02", day, h.now().Location())
	ch, items, err := h.challenge.Regenerate(c.Request.Context(), lang, level, dayTime)
	if err != nil {
		if errors.Is(err, store.ErrChallengeLocked) {
			writeError(c, http.StatusConflict, "关卡已发布或已有作答，不能重新生成")
			return
		}
		if errors.Is(err, challenge.ErrNoContent) {
			writeError(c, http.StatusUnprocessableEntity, "素材不足，无法生成关卡")
			return
		}
		h.logger.Printf("admin regenerate challenge %s/%s/%s: %v", lang, level, day, err)
		writeError(c, http.StatusInternalServerError, "regenerate challenge failed")
		return
	}
	views := make([]adminChallengeItem, 0, len(items))
	for _, item := range items {
		views = append(views, adminChallengeItem{
			Seq: item.Seq, Kind: item.Kind, RefID: item.RefID,
			Prompt: item.Prompt, Answer: item.Answer, Explanation: item.Explanation,
		})
	}
	writeJSON(c, http.StatusOK, map[string]any{"challenge": ch, "items": views})
}

// adminUpdateChallenge 修改标题或发布状态；发布后内容锁定。
func (h *Handler) adminUpdateChallenge(c *gin.Context) {
	lang, level, day, ok := h.parseAdminChallengeParams(c)
	if !ok {
		return
	}
	ch, err := h.store.GetChallengeByDay(c.Request.Context(), lang, level, day)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("admin update challenge get: %v", err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	var req struct {
		Title  *string `json:"title"`
		Status *string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		req.Title = &title
	}
	updated, err := h.store.UpdateChallenge(c.Request.Context(), ch.ID, req.Title, req.Status)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrChallengeLocked):
			writeError(c, http.StatusConflict, "关卡已发布或已有作答，不能修改")
		case err.Error() == "invalid status":
			writeError(c, http.StatusBadRequest, "status 只能是 draft 或 published")
		default:
			h.logger.Printf("admin update challenge: %v", err)
			writeError(c, http.StatusInternalServerError, "update challenge failed")
		}
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"challenge": updated})
}

// parseAdminChallengeParams 解析管理台路径里的 lang / level / day。
func (h *Handler) parseAdminChallengeParams(c *gin.Context) (string, string, string, bool) {
	lang := strings.TrimSpace(c.Query("lang"))
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return "", "", "", false
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if !challengeLevelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid or missing level")
		return "", "", "", false
	}
	day := strings.TrimSpace(c.Param("day"))
	if _, err := time.ParseInLocation("2006-01-02", day, h.now().Location()); err != nil {
		writeError(c, http.StatusBadRequest, "invalid day, want YYYY-MM-DD")
		return "", "", "", false
	}
	return lang, level, day, true
}
