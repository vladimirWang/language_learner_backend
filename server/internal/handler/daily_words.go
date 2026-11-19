package handler

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

//go:embed openapi/daily-words.json
var dailyWordsOpenAPI []byte

var levelPattern = regexp.MustCompile(`^[A-C][12]$`)

// DailyWordsConfig 是每日新词接口的运行配置。
type DailyWordsConfig struct {
	APIKey    string
	RateLimit int
	Location  *time.Location
}

type rateWindow struct {
	start time.Time
	count int
}

// now 返回 APP_TZ 下的当前时间；未配置时退回进程本地时区。
func (h *Handler) now() time.Time {
	if h.daily.Location != nil {
		return time.Now().In(h.daily.Location)
	}
	return time.Now()
}

// dailyWordsSpec 返回豆包插件使用的 OpenAPI 描述。
func (h *Handler) dailyWordsSpec(c *gin.Context) {
	c.Data(http.StatusOK, "application/json; charset=utf-8", dailyWordsOpenAPI)
}

// requireDailyWordsKey 校验 X-API-Key 与限流；未配置 key 时直接 503。
func (h *Handler) requireDailyWordsKey(c *gin.Context) bool {
	if h.daily.APIKey == "" {
		writeError(c, http.StatusServiceUnavailable, "daily words api key not configured")
		return false
	}
	provided := c.GetHeader("X-API-Key")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(h.daily.APIKey)) != 1 {
		writeError(c, http.StatusUnauthorized, "invalid api key")
		return false
	}
	if !h.allowDailyWords(c.ClientIP()) {
		writeError(c, http.StatusTooManyRequests, "rate limit exceeded")
		return false
	}
	return true
}

// allowDailyWords 按 IP 做 60 次/分钟的进程内近似限流；多实例部署时为近似值。
func (h *Handler) allowDailyWords(clientIP string) bool {
	limit := h.daily.RateLimit
	if limit <= 0 {
		return true
	}
	now := time.Now()
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	if h.rateHits == nil {
		h.rateHits = make(map[string]rateWindow)
	}
	window := h.rateHits[clientIP]
	if now.Sub(window.start) >= time.Minute {
		window = rateWindow{start: now}
	}
	window.count++
	h.rateHits[clientIP] = window
	// 定期清理过期窗口，避免长期运行内存增长。
	if len(h.rateHits) > 1024 {
		for ip, item := range h.rateHits {
			if now.Sub(item.start) >= time.Minute {
				delete(h.rateHits, ip)
			}
		}
	}
	return window.count <= limit
}

// dailyWords 返回当天分配的单词；同一天同一 device 结果稳定。
func (h *Handler) dailyWords(c *gin.Context) {
	if !h.requireDailyWordsKey(c) {
		return
	}
	lang := strings.TrimSpace(c.Query("lang"))
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level != "" && !levelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	count, countErr := parseDailyWordsCount(c.Query("count"))
	if countErr != "" {
		writeError(c, http.StatusBadRequest, countErr)
		return
	}
	deviceID := strings.TrimSpace(c.Query("device_id"))

	now := h.now()
	words, err := h.store.DailyWords(c.Request.Context(), lang, level, count, deviceID, now)
	if err != nil {
		h.logger.Printf("daily words lang=%s: %v", lang, err)
		writeError(c, http.StatusInternalServerError, "query daily words failed")
		return
	}
	resp := map[string]any{
		"date":  now.Format("2006-01-02"),
		"lang":  lang,
		"level": level,
		"words": words,
	}
	if len(words) == 0 {
		resp["reason"] = "vocab_not_ready"
	}
	writeJSON(c, http.StatusOK, resp)
}

// markWordsLearned 记录设备已学的新词，供“最近五天新词”使用。
func (h *Handler) markWordsLearned(c *gin.Context) {
	if !h.requireDailyWordsKey(c) {
		return
	}
	var req struct {
		DeviceID string  `json:"device_id"`
		WordIDs  []int64 `json:"word_ids"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		writeError(c, http.StatusBadRequest, "device_id is required")
		return
	}
	ids := dedupeInt64(req.WordIDs)
	if len(ids) == 0 || len(ids) > 20 {
		writeError(c, http.StatusBadRequest, "word_ids must contain 1-20 items")
		return
	}

	accepted, ignored, err := h.store.MarkWordsLearned(c.Request.Context(), deviceID, ids)
	if err != nil {
		h.logger.Printf("mark words learned device=%s: %v", deviceID, err)
		writeError(c, http.StatusInternalServerError, "mark words learned failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]int{"accepted": accepted, "ignored": ignored})
}

// listGrammarPoints 返回可选语法点，供管理台配置方案级语法要求。
func (h *Handler) listGrammarPoints(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	if !langPattern.MatchString(lang) {
		writeError(c, http.StatusBadRequest, "invalid or missing lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level != "" && !levelPattern.MatchString(level) {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	points, err := h.store.ListGrammarPoints(c.Request.Context(), lang, level)
	if err != nil {
		h.logger.Printf("list grammar points lang=%s: %v", lang, err)
		writeError(c, http.StatusInternalServerError, "list grammar points failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"points": points})
}

// checkGrammarRequirements 只对本次回答做匹配；已达成集合由客户端累计传入。
func (h *Handler) checkGrammarRequirements(c *gin.Context) {
	var req struct {
		PlanID     int64    `json:"plan_id"`
		Level      string   `json:"level"`
		Answer     string   `json:"answer"`
		AlreadyMet []string `json:"already_met"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.PlanID <= 0 {
		writeError(c, http.StatusBadRequest, "plan_id is required")
		return
	}

	requirements, err := h.store.GetGrammarRequirements(c.Request.Context(), req.PlanID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "speaking plan not found")
			return
		}
		h.logger.Printf("load grammar requirements plan=%d: %v", req.PlanID, err)
		writeError(c, http.StatusInternalServerError, "load grammar requirements failed")
		return
	}

	alreadyMet := make(map[string]bool, len(req.AlreadyMet))
	for _, key := range req.AlreadyMet {
		alreadyMet[strings.TrimSpace(key)] = true
	}

	type result struct {
		Key      string   `json:"key"`
		Matched  bool     `json:"matched"`
		Count    int      `json:"count"`
		Evidence []string `json:"evidence"`
	}
	results := make([]result, 0, len(requirements))
	missing := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		evidence := make([]string, 0, 1)
		for _, pattern := range requirement.Patterns {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				h.logger.Printf("invalid grammar pattern plan=%d key=%s: %v", req.PlanID, requirement.Key, err)
				continue
			}
			if matched := compiled.FindString(req.Answer); matched != "" {
				evidence = append(evidence, matched)
				break
			}
		}
		count := 0
		if len(evidence) > 0 {
			count = 1
		}
		matched := count > 0 || alreadyMet[requirement.Key]
		results = append(results, result{
			Key:      requirement.Key,
			Matched:  matched,
			Count:    count,
			Evidence: evidence,
		})
		if requirement.Enforce != "warn" && !matched {
			missing = append(missing, requirement.Key)
		}
	}

	writeJSON(c, http.StatusOK, map[string]any{
		"results":          results,
		"all_blocking_met": len(missing) == 0,
		"missing":          missing,
	})
}

// parseDailyWordsCount 解析 count 参数：缺省 5，越界或非数字返回错误信息。
func parseDailyWordsCount(raw string) (int, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 5, ""
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 || count > maxDailyWordsCount {
		return 0, "count must be an integer between 1 and 20"
	}
	return count, ""
}

const maxDailyWordsCount = 20

func dedupeInt64(values []int64) []int64 {
	out := make([]int64, 0, len(values))
	seen := make(map[int64]bool, len(values))
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// normalizeGrammarRequirements 校验并规范化方案级语法要求，最多 3 条。
func normalizeGrammarRequirements(raw json.RawMessage) (json.RawMessage, string) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return json.RawMessage("[]"), ""
	}
	var requirements []store.GrammarRequirement
	if err := json.Unmarshal(raw, &requirements); err != nil {
		return nil, "invalid grammar_requirements: " + err.Error()
	}
	if len(requirements) > 3 {
		return nil, "grammar_requirements supports at most 3 items"
	}
	seenKeys := make(map[string]bool, len(requirements))
	for i := range requirements {
		item := &requirements[i]
		item.Key = strings.TrimSpace(item.Key)
		item.Label = strings.TrimSpace(item.Label)
		item.SourcePath = strings.TrimSpace(item.SourcePath)
		item.Enforce = strings.TrimSpace(item.Enforce)
		// 引导问句由语法点清单实时提供，不固化进方案。
		item.Prompts = nil
		if item.Key == "" {
			return nil, fmt.Sprintf("grammar_requirements[%d].key is required", i)
		}
		if len([]rune(item.Key)) > 64 {
			return nil, fmt.Sprintf("grammar_requirements[%d].key is too long (max 64)", i)
		}
		if seenKeys[item.Key] {
			return nil, fmt.Sprintf("duplicate grammar requirement key: %s", item.Key)
		}
		seenKeys[item.Key] = true
		if item.Label == "" {
			return nil, fmt.Sprintf("grammar_requirements[%d].label is required", i)
		}
		if len([]rune(item.Label)) > 80 {
			return nil, fmt.Sprintf("grammar_requirements[%d].label is too long (max 80)", i)
		}
		if item.Enforce == "" {
			item.Enforce = "block"
		}
		if item.Enforce != "block" && item.Enforce != "warn" {
			return nil, fmt.Sprintf("grammar_requirements[%d].enforce must be block or warn", i)
		}
		patterns := make([]string, 0, len(item.Patterns))
		for _, pattern := range item.Patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if len([]rune(pattern)) > 200 {
				return nil, fmt.Sprintf("grammar_requirements[%d].patterns item is too long (max 200)", i)
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, fmt.Sprintf("grammar_requirements[%d].patterns invalid: %v", i, err)
			}
			patterns = append(patterns, pattern)
		}
		// 无判定规则的语法点只能作为提示，不能阻断。
		if len(patterns) == 0 && item.Enforce == "block" {
			return nil, fmt.Sprintf("grammar_requirements[%d].patterns is required when enforce is block", i)
		}
		item.Patterns = patterns
		if item.MinUses == 0 {
			item.MinUses = 1
		}
		if item.MinUses < 1 || item.MinUses > 10 {
			return nil, fmt.Sprintf("grammar_requirements[%d].min_uses must be 1-10", i)
		}
		hints := make([]string, 0, len(item.HintParts))
		seenHints := make(map[string]bool, len(item.HintParts))
		for _, hint := range item.HintParts {
			hint = strings.TrimSpace(hint)
			if hint == "" || seenHints[hint] {
				continue
			}
			seenHints[hint] = true
			hints = append(hints, hint)
		}
		item.HintParts = hints
	}
	out, err := json.Marshal(requirements)
	if err != nil {
		return nil, "invalid grammar_requirements"
	}
	return out, ""
}
