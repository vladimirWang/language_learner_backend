package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

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

// adminListChallenges 返回某语种/等级下的关卡序列（含草稿），附参与人数。
func (h *Handler) adminListChallenges(c *gin.Context) {
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
	list, err := h.store.ListChallengesForAdmin(c.Request.Context(), lang, level)
	if err != nil {
		h.logger.Printf("admin list challenges: %v", err)
		writeError(c, http.StatusInternalServerError, "list challenges failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"challenges": list})
}

// adminCreateChallenge 在序列末尾新增一个草稿关卡。
func (h *Handler) adminCreateChallenge(c *gin.Context) {
	var req struct {
		Lang          string `json:"lang"`
		Level         string `json:"level"`
		ChallengeType string `json:"challenge_type"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	lang := strings.TrimSpace(req.Lang)
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(req.Level))
	if !challengeLevelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid or missing level")
		return
	}
	requestedType := strings.TrimSpace(req.ChallengeType)
	switch requestedType {
	case "", store.ChallengeTypeVocab, store.ChallengeTypeGrammar, store.ChallengeTypeListening:
	default:
		writeError(c, http.StatusBadRequest, "challenge_type 只能是 vocab、grammar 或 listening")
		return
	}
	ch, _, err := h.challenge.CreateDraft(c.Request.Context(), lang, level, requestedType)
	if err != nil {
		if errors.Is(err, challenge.ErrNoContent) {
			writeError(c, http.StatusUnprocessableEntity, "素材不足，无法生成关卡")
			return
		}
		h.logger.Printf("admin create challenge %s/%s: %v", lang, level, err)
		writeError(c, http.StatusInternalServerError, "create challenge failed")
		return
	}
	h.writeAdminChallengeDetail(c, ch.ID)
}

// adminGetChallenge 返回某关卡详情，包含标准答案与参与人数。
func (h *Handler) adminGetChallenge(c *gin.Context) {
	id, ok := h.parseAdminChallengeID(c)
	if !ok {
		return
	}
	ch, err := h.store.GetChallengeForAdmin(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("admin get challenge: %v", err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	h.writeAdminChallengeDetail(c, ch.ID)
}

// adminRegenerateChallenge 重新生成草稿内容；已发布关卡不允许修改。
func (h *Handler) adminRegenerateChallenge(c *gin.Context) {
	id, ok := h.parseAdminChallengeID(c)
	if !ok {
		return
	}
	requestedType := strings.TrimSpace(c.Query("challenge_type"))
	switch requestedType {
	case "", store.ChallengeTypeVocab, store.ChallengeTypeGrammar, store.ChallengeTypeListening:
	default:
		writeError(c, http.StatusBadRequest, "challenge_type 只能是 vocab、grammar 或 listening")
		return
	}
	ch, _, err := h.challenge.RegenerateByID(c.Request.Context(), id, requestedType)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(c, http.StatusNotFound, "challenge not found")
		case errors.Is(err, store.ErrChallengeLocked):
			writeError(c, http.StatusConflict, "关卡已发布或已有作答，不能重新生成")
		case errors.Is(err, challenge.ErrNoContent):
			writeError(c, http.StatusUnprocessableEntity, "素材不足，无法生成关卡")
		default:
			h.logger.Printf("admin regenerate challenge id=%d: %v", id, err)
			writeError(c, http.StatusInternalServerError, "regenerate challenge failed")
		}
		return
	}
	h.writeAdminChallengeDetail(c, ch.ID)
}

// adminUpdateChallenge 修改标题或发布状态；发布后内容锁定。
func (h *Handler) adminUpdateChallenge(c *gin.Context) {
	id, ok := h.parseAdminChallengeID(c)
	if !ok {
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
	updated, err := h.store.UpdateChallenge(c.Request.Context(), id, req.Title, req.Status)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(c, http.StatusNotFound, "challenge not found")
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
	adminCh, err := h.store.GetChallengeForAdmin(c.Request.Context(), updated.ID)
	if err != nil {
		h.logger.Printf("admin update challenge reload: %v", err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"challenge": adminCh})
}

// writeAdminChallengeDetail 按 id 读取管理台详情（含序号与参与人数）并返回全部题目。
func (h *Handler) writeAdminChallengeDetail(c *gin.Context, id int64) {
	ctx := c.Request.Context()
	ch, err := h.store.GetChallengeForAdmin(ctx, id)
	if err != nil {
		h.logger.Printf("admin load challenge id=%d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	items, err := h.store.ListChallengeItems(ctx, id)
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

// parseAdminChallengeID 解析管理台路径里的关卡 ID。
func (h *Handler) parseAdminChallengeID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid challenge id")
		return 0, false
	}
	return id, true
}
