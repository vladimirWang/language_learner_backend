package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// examIDPattern 限制任务 ID 只能是安全字符，避免拼路径时越界。
var examIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// listeningQuestionView 是听力任务的单题视图，素材地址已转成小程序可请求的 URL。
type listeningQuestionView struct {
	Number     int      `json:"number"`
	Duration   float64  `json:"duration"`
	AudioURL   string   `json:"audio_url"`
	ImageURL   string   `json:"image_url"`
	Transcript string   `json:"transcript"`
	Answer     string   `json:"answer"`
	Options    []string `json:"options"`
}

// listeningContentView 是听力任务的展示内容。
type listeningContentView struct {
	Duration      float64                 `json:"duration"`
	QuestionCount int                     `json:"question_count"`
	Questions     []listeningQuestionView `json:"questions"`
}

// speakingItemView 是口语题目视图。按项目约定不下发 lang。
type speakingItemView struct {
	ItemKey        string `json:"item_key"`
	Topic          string `json:"topic"`
	Role           string `json:"role"`
	PromptDE       string `json:"prompt_de"`
	PromptZH       string `json:"prompt_zh"`
	ExpectedPoints string `json:"expected_points"`
	RefAnswer      string `json:"ref_answer"`
	ErrorNote      string `json:"error_note"`
	SortOrder      int    `json:"sort_order"`
}

// speakingPlanView 是口语方案视图（不含 lang）。
type speakingPlanView struct {
	ID                  int64           `json:"id"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	GrammarRequirements json.RawMessage `json:"grammar_requirements"`
}

// speakingContentView 是口语任务（单题 / 话题 / 方案）的展示内容。
type speakingContentView struct {
	Plan  *speakingPlanView  `json:"plan,omitempty"`
	Items []speakingItemView `json:"items"`
}

// testItemContent 返回测试中某个任务的展示内容。
// GET /api/tests/:id/items/:itemType/:examId
func (h *Handler) testItemContent(c *gin.Context) {
	levelID, err := parseLevelID(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	itemType := c.Param("itemType")
	examID := c.Param("examId")
	if !examIDPattern.MatchString(examID) {
		writeError(c, http.StatusBadRequest, "invalid exam id")
		return
	}

	exams, err := h.store.ListLevelExams(c.Request.Context(), levelID)
	if err != nil {
		h.logger.Printf("list level exams %d: %v", levelID, err)
		writeError(c, http.StatusInternalServerError, "load test items failed")
		return
	}
	var linked *store.LevelExamInfo
	for i := range exams {
		if exams[i].ItemType == itemType && exams[i].ExamID == examID {
			linked = &exams[i]
			break
		}
	}
	if linked == nil {
		writeError(c, http.StatusNotFound, "test item not found")
		return
	}

	switch itemType {
	case "listening":
		h.writeListeningContent(c, levelID, examID, linked.Name)
	case "speaking", "speaking_topic":
		item, err := h.store.GetSpeakingItem(c.Request.Context(), examID)
		if err != nil {
			h.logger.Printf("get speaking item %s: %v", examID, err)
			writeError(c, http.StatusInternalServerError, "load speaking item failed")
			return
		}
		if item == nil || !item.Active {
			writeError(c, http.StatusNotFound, "speaking item not found")
			return
		}
		title := strings.TrimSpace(item.Topic)
		if title == "" {
			title = item.PromptDE
		}
		writeJSON(c, http.StatusOK, map[string]any{
			"item_type": itemType,
			"exam_id":   examID,
			"title":     title,
			"speaking": speakingContentView{
				Items: []speakingItemView{speakingItemFromStore(item)},
			},
		})
	case "speaking_plan":
		planID, convErr := strconv.ParseInt(examID, 10, 64)
		if convErr != nil || planID <= 0 {
			writeError(c, http.StatusBadRequest, "invalid speaking plan id")
			return
		}
		plan, items, err := h.store.GetSpeakingPlan(c.Request.Context(), planID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(c, http.StatusNotFound, "speaking plan not found")
				return
			}
			h.logger.Printf("get speaking plan %d: %v", planID, err)
			writeError(c, http.StatusInternalServerError, "load speaking plan failed")
			return
		}
		view := speakingContentView{
			Plan: &speakingPlanView{
				ID:                  plan.ID,
				Name:                plan.Name,
				Description:         plan.Description,
				GrammarRequirements: json.RawMessage(plan.GrammarRequirements),
			},
			Items: make([]speakingItemView, 0, len(items)),
		}
		for i := range items {
			view.Items = append(view.Items, speakingItemFromStore(&items[i]))
		}
		writeJSON(c, http.StatusOK, map[string]any{
			"item_type": itemType,
			"exam_id":   examID,
			"title":     plan.Name,
			"speaking":  view,
		})
	default:
		writeError(c, http.StatusBadRequest, "unsupported item type")
	}
}

// writeListeningContent 读取 builder.listen 产出的 manifest.json，转成展示内容。
// lang 只在服务端使用，不下发。
func (h *Handler) writeListeningContent(c *gin.Context, levelID int64, examID, fallbackTitle string) {
	dir := listeningExamDirByID(examID)
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		h.logger.Printf("read listening manifest %s: %v", examID, err)
		writeError(c, http.StatusNotFound, "听力素材尚未就绪")
		return
	}
	var manifest struct {
		Title         string  `json:"title"`
		Duration      float64 `json:"duration"`
		QuestionCount int     `json:"question_count"`
		Questions     []struct {
			Number     int      `json:"number"`
			Start      float64  `json:"start"`
			End        float64  `json:"end"`
			Audio      string   `json:"audio"`
			Image      *string  `json:"image"`
			Transcript string   `json:"transcript"`
			Answer     *string  `json:"answer"`
			Options    []string `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		h.logger.Printf("decode listening manifest %s: %v", examID, err)
		writeError(c, http.StatusInternalServerError, "听力素材格式异常")
		return
	}

	assetPrefix := fmt.Sprintf("/api/tests/%d/items/listening/%s/asset?path=",
		levelID, url.PathEscape(examID))
	questions := make([]listeningQuestionView, 0, len(manifest.Questions))
	for _, q := range manifest.Questions {
		view := listeningQuestionView{
			Number:     q.Number,
			Duration:   math.Round((q.End-q.Start)*100) / 100,
			Transcript: q.Transcript,
			Options:    q.Options,
		}
		if view.Options == nil {
			view.Options = []string{}
		}
		if q.Audio != "" {
			view.AudioURL = assetPrefix + url.QueryEscape(q.Audio)
		}
		if q.Image != nil && *q.Image != "" {
			view.ImageURL = assetPrefix + url.QueryEscape(*q.Image)
		}
		if q.Answer != nil {
			view.Answer = *q.Answer
		}
		questions = append(questions, view)
	}

	title := strings.TrimSpace(manifest.Title)
	if title == "" {
		title = fallbackTitle
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"item_type": "listening",
		"exam_id":   examID,
		"title":     title,
		"listening": listeningContentView{
			Duration:      manifest.Duration,
			QuestionCount: len(questions),
			Questions:     questions,
		},
	})
}

// testItemAsset 返回听力任务目录内的素材文件。
// GET /api/tests/:id/items/:itemType/:examId/asset?path=audio/q01.m4a
func (h *Handler) testItemAsset(c *gin.Context) {
	if _, err := parseLevelID(c); err != nil {
		writeError(c, http.StatusBadRequest, "invalid level id")
		return
	}
	if c.Param("itemType") != "listening" {
		writeError(c, http.StatusBadRequest, "unsupported item type")
		return
	}
	examID := c.Param("examId")
	if !examIDPattern.MatchString(examID) {
		writeError(c, http.StatusBadRequest, "invalid exam id")
		return
	}

	target, err := safeJoin(listeningExamDirByID(examID), c.Query("path"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		writeError(c, http.StatusNotFound, "file not found")
		return
	}

	c.Header("Content-Type", contentTypeFor(target))
	if isInlinePlayable(target) {
		c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(target)))
	} else {
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(target)))
	}
	c.Header("X-Accel-Buffering", "no")
	c.File(target)
}

// listeningExamDirByID 返回 WORK_DIR/listening/{exam_id} 目录。
func listeningExamDirByID(examID string) string {
	workRoot := os.Getenv("WORK_DIR")
	if workRoot == "" {
		workRoot = "work"
	}
	return filepath.Join(workRoot, "listening", examID)
}

// speakingItemFromStore 把口语题目转成展示视图，刻意不带 lang/level。
func speakingItemFromStore(item *store.SpeakingItem) speakingItemView {
	return speakingItemView{
		ItemKey:        item.ItemKey,
		Topic:          item.Topic,
		Role:           item.Role,
		PromptDE:       item.PromptDE,
		PromptZH:       item.PromptZH,
		ExpectedPoints: item.ExpectedPoints,
		RefAnswer:      item.RefAnswer,
		ErrorNote:      item.ErrorNote,
		SortOrder:      item.SortOrder,
	}
}
