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

// challengeStars 标准 6 题每 2 题 1 星；不足 6 题按正确率折算并执行 60% 向下取整兜底。
func challengeStars(score, total int) int {
	if total <= 0 || score <= 0 {
		return 0
	}
	stars := 0
	if total >= 6 {
		stars = score / 2
	} else {
		stars = score * 3 / total
		if score >= total*3/5 && stars < 1 {
			stars = 1
		}
	}
	if stars > 3 {
		return 3
	}
	return stars
}

// requireWXUser 从 Bearer token 解析并读取微信用户。
func (h *Handler) requireWXUser(c *gin.Context) (store.WXUser, bool) {
	if !h.wx.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "微信登录未配置")
		return store.WXUser{}, false
	}
	token := bearerToken(c)
	if token == "" {
		writeError(c, http.StatusUnauthorized, "unauthorized")
		return store.WXUser{}, false
	}
	uid, err := h.wx.VerifyToken(token, time.Now())
	if err != nil {
		writeError(c, http.StatusUnauthorized, "login expired")
		return store.WXUser{}, false
	}
	u, err := h.store.GetWXUserByID(c.Request.Context(), uid)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "user not found")
		return store.WXUser{}, false
	}
	return u, true
}

// requireWXOpenID 从 Bearer token 解析微信用户 openid；未登录返回 401。
func (h *Handler) requireWXOpenID(c *gin.Context) (string, bool) {
	u, ok := h.requireWXUser(c)
	return u.OpenID, ok
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
	user, err := h.store.GetWXUserByOpenID(c.Request.Context(), openid)
	if err != nil {
		h.logger.Printf("daily challenge user openid=%s: %v", openid, err)
		writeError(c, http.StatusInternalServerError, "load user failed")
		return
	}
	day := h.todayDay()
	resp := map[string]any{
		"date":        day.Format("2006-01-02"),
		"streak":      h.currentStreak(c.Request.Context(), openid, lang),
		"total_stars": user.TotalStars,
	}
	ch, items, err := h.challenge.EnsureDay(c.Request.Context(), lang, level, day, true)
	if err != nil {
		if errors.Is(err, challenge.ErrNoContent) {
			resp["challenge"] = nil
			resp["reason"] = "content_not_ready"
			writeJSON(c, http.StatusOK, resp)
			return
		}
		h.logger.Printf("daily challenge today lang=%s level=%s: %v", lang, level, err)
		writeError(c, http.StatusInternalServerError, "load daily challenge failed")
		return
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
			"answers":    attempt.Answers,
			"score":      attempt.Score,
			"total":      attempt.Total,
			"stars":      attempt.Stars,
			"best_stars": attempt.BestStars,
			"passed":     attempt.Passed,
			"checked_in": attempt.Passed,
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

// submitDailyChallenge 判分并保存最新作答；重复提交按历史最高星补差。
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
	if ch.Day > h.todayDay().Format("2006-01-02") {
		writeError(c, http.StatusForbidden, "challenge not yet available")
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
	stars := challengeStars(score, total)

	submission, err := h.store.SubmitChallengeAttempt(c.Request.Context(), store.ChallengeAttempt{
		OpenID:      openid,
		ChallengeID: id,
		Answers:     rawAnswers,
		Score:       score,
		Total:       total,
		Stars:       stars,
	}, ch.Lang, ch.Day)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "challenge not found")
			return
		}
		h.logger.Printf("submit challenge save id=%d openid=%s: %v", id, openid, err)
		writeError(c, http.StatusInternalServerError, "save attempt failed")
		return
	}
	attempt := submission.Attempt
	streak := store.NormalizeStreak(submission.Streak, h.todayDay().Format("2006-01-02"))
	resp := map[string]any{
		"score":             attempt.Score,
		"total":             attempt.Total,
		"stars":             attempt.Stars,
		"best_stars":        attempt.BestStars,
		"stars_added":       submission.StarsAdded,
		"total_stars":       submission.TotalStars,
		"passed":            attempt.Passed,
		"checked_in":        attempt.Passed,
		"already_submitted": submission.AlreadySubmitted,
		"items":             itemResults,
		"streak":            streak,
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
	today := h.todayDay().Format("2006-01-02")
	if from > today {
		writeJSON(c, http.StatusOK, map[string]any{"month": month, "entries": []store.ChallengeCalendarEntry{}})
		return
	}
	if to > today {
		to = today
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
