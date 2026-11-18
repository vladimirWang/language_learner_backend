package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

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
