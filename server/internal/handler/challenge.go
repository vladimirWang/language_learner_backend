package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/challenge"
	"github.com/wangfernando/language-learner/server/internal/store"
)

var challengeLevelPattern = regexp.MustCompile(`^(A[12]|B[12]|C[12]|N[1-5])$`)

// passRatio 打卡及格线：答对率 ≥ 80%。
const passRatioNum, passRatioDen = 4, 5

// requireWXOpenID 从 Bearer token 解析微信用户 openid；未登录返回 401。
func (h *Handler) requireWXOpenID(c *gin.Context) (string, bool) {
	if !h.wx.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "微信登录未配置")
		return "", false
	}
	token := bearerToken(c)
	if token == "" {
		writeError(c, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	uid, err := h.wx.VerifyToken(token, time.Now())
	if err != nil {
		writeError(c, http.StatusUnauthorized, "login expired")
		return "", false
	}
	u, err := h.store.GetWXUserByID(c.Request.Context(), uid)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "user not found")
		return "", false
	}
	return u.OpenID, true
}

// parseChallengeLangLevel 校验关卡的语种与等级参数。
func parseChallengeLangLevel(c *gin.Context) (string, string, bool) {
	lang := strings.TrimSpace(c.Query("lang"))
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return "", "", false
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if !challengeLevelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid or missing level")
		return "", "", false
	}
	return lang, level, true
}

// todayDay 返回 APP_TZ 下的今天（只保留日期）。
func (h *Handler) todayDay() time.Time {
	now := h.now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// currentStreak 读取并按今天日期修正连续打卡天数（断档显示为 0）。
func (h *Handler) currentStreak(ctx context.Context, openid, lang string) store.UserStreak {
	streak, err := h.store.GetStreak(ctx, openid, lang)
	if err != nil {
		h.logger.Printf("daily challenge streak openid=%s: %v", openid, err)
		return store.UserStreak{Lang: lang}
	}
	return store.NormalizeStreak(streak, h.todayDay().Format("2006-01-02"))
}

// challengeItemView 是下发给客户端的题目（不含标准答案）。
type challengeItemView struct {
	Seq    int             `json:"seq"`
	Kind   string          `json:"kind"`
	Prompt json.RawMessage `json:"prompt"`
}

// dailyChallengeToday 返回当天关卡、当前用户的作答与连续打卡。
func (h *Handler) dailyChallengeToday(c *gin.Context) {
	openid, ok := h.requireWXOpenID(c)
	if !ok {
		return
	}
	lang, level, ok := parseChallengeLangLevel(c)
	if !ok {
		return
	}
	day := h.todayDay()
	ch, items, err := h.challenge.EnsureDay(c.Request.Context(), lang, level, day, true)
	if err != nil {
		if errors.Is(err, challenge.ErrNoContent) {
			writeJSON(c, http.StatusOK, map[string]any{
				"date":      day.Format("2006-01-02"),
				"challenge": nil,
				"reason":    "content_not_ready",
			})
			return
		}
		h.logger.Printf("daily challenge today lang=%s level=%s: %v", lang, level, err)
		writeError(c, http.StatusInternalServerError, "load daily challenge failed")
		return
	}

	streak := h.currentStreak(c.Request.Context(), openid, lang)
	resp := map[string]any{
		"date":   day.Format("2006-01-02"),
		"streak": streak,
	}
	if ch.Status != store.ChallengeStatusPublished {
		resp["challenge"] = nil
		resp["reason"] = "not_published"
		writeJSON(c, http.StatusOK, resp)
		return
	}

	views := make([]challengeItemView, 0, len(items))
	for _, item := range items {
		views = append(views, challengeItemView{Seq: item.Seq, Kind: item.Kind, Prompt: item.Prompt})
	}
	resp["challenge"] = map[string]any{
		"id":    ch.ID,
		"day":   ch.Day,
		"title": ch.Title,
		"items": views,
	}
	if attempt, err := h.store.GetAttempt(c.Request.Context(), openid, ch.ID); err == nil {
		resp["attempt"] = map[string]any{
			"score":  attempt.Score,
			"total":  attempt.Total,
			"passed": attempt.Passed,
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		h.logger.Printf("daily challenge attempt openid=%s: %v", openid, err)
	}
	writeJSON(c, http.StatusOK, resp)
}

// submitAnswer 是客户端提交的单题答案（value 为所选选项文本）。
type submitAnswer struct {
	Seq   int    `json:"seq"`
	Value string `json:"value"`
}

type submitChallengeReq struct {
	Answers []submitAnswer `json:"answers"`
}

// submitDailyChallenge 判分并记录作答；重复提交返回首次结果。
func (h *Handler) submitDailyChallenge(c *gin.Context) {
	openid, ok := h.requireWXOpenID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid challenge id")
		return
	}
	var req submitChallengeReq
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}

	ch, err := h.store.GetChallengeByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("submit challenge get id=%d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "load challenge failed")
		return
	}
	if ch.Status != store.ChallengeStatusPublished {
		writeError(c, http.StatusForbidden, "challenge not published")
		return
	}
	items, err := h.store.ListChallengeItems(c.Request.Context(), id)
	if err != nil {
		h.logger.Printf("submit challenge items id=%d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "load challenge items failed")
		return
	}

	rawAnswers, err := json.Marshal(req.Answers)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid answers")
		return
	}
	score, itemResults := gradeChallenge(items, req.Answers)
	total := len(items)
	passed := total > 0 && score*passRatioDen >= total*passRatioNum

	attempt, saved, err := h.store.SaveAttempt(c.Request.Context(), store.ChallengeAttempt{
		OpenID:      openid,
		ChallengeID: id,
		Answers:     rawAnswers,
		Score:       score,
		Total:       total,
		Passed:      passed,
	})
	if err != nil {
		h.logger.Printf("submit challenge save id=%d openid=%s: %v", id, openid, err)
		writeError(c, http.StatusInternalServerError, "save attempt failed")
		return
	}

	resp := map[string]any{
		"score":            attempt.Score,
		"total":            attempt.Total,
		"passed":           attempt.Passed,
		"already_submitted": !saved,
		"items":            itemResults,
	}
	if saved {
		if attempt.Passed {
			streak, err := h.store.UpdateStreakOnPass(c.Request.Context(), openid, ch.Lang, ch.Day)
			if err != nil {
				h.logger.Printf("submit challenge streak openid=%s: %v", openid, err)
			} else {
				resp["streak"] = streak
			}
		}
	} else {
		// 重复提交：按首次答案重新计算逐题结果，保证响应一致。
		var stored []submitAnswer
		if err := json.Unmarshal(attempt.Answers, &stored); err == nil {
			_, resp["items"] = gradeChallenge(items, stored)
		}
	}
	if _, exists := resp["streak"]; !exists {
		resp["streak"] = h.currentStreak(c.Request.Context(), openid, ch.Lang)
	}
	writeJSON(c, http.StatusOK, resp)
}

// challengeItemResult 是逐题判分结果。
type challengeItemResult struct {
	Seq           int    `json:"seq"`
	Kind          string `json:"kind"`
	Correct       bool   `json:"correct"`
	CorrectAnswer string `json:"correct_answer"`
	Explanation   string `json:"explanation"`
}

// gradeChallenge 对比答案，返回得分与逐题结果。
func gradeChallenge(items []store.DailyChallengeItem, answers []submitAnswer) (int, []challengeItemResult) {
	selected := make(map[int]string, len(answers))
	for _, a := range answers {
		selected[a.Seq] = normalizeChallengeAnswer(a.Value)
	}
	score := 0
	results := make([]challengeItemResult, 0, len(items))
	for _, item := range items {
		var ans struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(item.Answer, &ans)
		correct := selected[item.Seq] != "" && selected[item.Seq] == normalizeChallengeAnswer(ans.Value)
		if correct {
			score++
		}
		results = append(results, challengeItemResult{
			Seq:           item.Seq,
			Kind:          item.Kind,
			Correct:       correct,
			CorrectAnswer: ans.Value,
			Explanation:   item.Explanation,
		})
	}
	return score, results
}

func normalizeChallengeAnswer(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

// dailyChallengeCalendar 返回某月已发布关卡与当前用户的完成情况。
func (h *Handler) dailyChallengeCalendar(c *gin.Context) {
	openid, ok := h.requireWXOpenID(c)
	if !ok {
		return
	}
	lang, level, ok := parseChallengeLangLevel(c)
	if !ok {
		return
	}
	from, to, month, ok := h.parseChallengeMonth(c)
	if !ok {
		return
	}
	entries, err := h.store.ChallengeCalendar(c.Request.Context(), openid, lang, level, from, to)
	if err != nil {
		h.logger.Printf("daily challenge calendar openid=%s: %v", openid, err)
		writeError(c, http.StatusInternalServerError, "load calendar failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"month": month, "entries": entries})
}

// dailyChallengeStreak 返回当前用户某语种的连续打卡天数。
func (h *Handler) dailyChallengeStreak(c *gin.Context) {
	openid, ok := h.requireWXOpenID(c)
	if !ok {
		return
	}
	lang := strings.TrimSpace(c.Query("lang"))
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return
	}
	streak := h.currentStreak(c.Request.Context(), openid, lang)
	writeJSON(c, http.StatusOK, map[string]any{"streak": streak})
}

// parseChallengeMonth 解析 ?month=YYYY-MM，缺省为当前月；返回当月首尾日期。
func (h *Handler) parseChallengeMonth(c *gin.Context) (string, string, string, bool) {
	now := h.now()
	month := strings.TrimSpace(c.Query("month"))
	if month == "" {
		month = now.Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, now.Location())
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid month, want YYYY-MM")
		return "", "", "", false
	}
	end := start.AddDate(0, 1, -1)
	return start.Format("2006-01-02"), end.Format("2006-01-02"), month, true
}
