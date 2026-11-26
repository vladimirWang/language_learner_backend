package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

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
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "level not found")
			return
		}
		if errors.Is(err, store.ErrExamNotFound) {
			writeError(c, http.StatusNotFound, "题目不存在或已删除")
			return
		}
		if errors.Is(err, store.ErrExamLevelMismatch) {
			writeError(c, http.StatusBadRequest, "只能添加与测试语种、等级匹配的题目")
			return
		}
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
