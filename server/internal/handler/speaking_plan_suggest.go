package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// 智能生成口语方案的辅助能力：按名称联想具体候选方案、自动生成方案说明。
// 两者都是轻量 LLM 调用，不写库，可被前端防抖/换一批反复调用。

type speakingPlanSuggestRequest struct {
	Lang     string   `json:"lang"`
	Level    string   `json:"level"`
	Name     string   `json:"name"`
	Previous []string `json:"previous"` // 换一批时需要避开的已有候选
}

// normalizeSuggestInput 校验并规范化公共输入。
func normalizeSuggestInput(req *speakingPlanSuggestRequest) string {
	req.Lang = strings.TrimSpace(req.Lang)
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		return "invalid lang"
	}
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	if req.Level == "" {
		req.Level = "A1"
	}
	if !validCefrLevels[req.Level] {
		return "invalid level (A1/A2/B1/B2/C1/C2)"
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return "name is required"
	}
	if len([]rune(req.Name)) > 100 {
		return "name is too long (max 100)"
	}
	previous := make([]string, 0, len(req.Previous))
	for _, item := range req.Previous {
		if item = strings.TrimSpace(item); item != "" {
			previous = append(previous, item)
		}
	}
	if len(previous) > 10 {
		previous = previous[:10]
	}
	req.Previous = previous
	return ""
}

// suggestSpeakingPlanNames 把粗略主题联想成若干具体候选方案名称。
func (h *Handler) suggestSpeakingPlanNames(c *gin.Context) {
	var req speakingPlanSuggestRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if msg := normalizeSuggestInput(&req); msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	if !h.llm.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "LLM 未配置，无法智能联想")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"lang":    req.Lang,
		"level":   req.Level,
		"topic":   req.Name,
		"exclude": req.Previous,
	})
	raw, err := h.llm.ChatJSON(
		c.Request.Context(),
		planNameSuggestSystemPrompt(req.Lang, req.Level),
		"请围绕以下主题联想候选方案：\n"+string(payload),
		600,
	)
	if err != nil {
		h.logger.Printf("suggest speaking plan names llm: %v", err)
		writeError(c, http.StatusBadGateway, "候选方案生成失败")
		return
	}
	candidates, err := parsePlanNameCandidates(raw)
	if err != nil {
		h.logger.Printf("suggest speaking plan names output rejected: %v", err)
		writeError(c, http.StatusBadGateway, "候选方案结果无效："+err.Error())
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"candidates": candidates})
}

// suggestSpeakingPlanDescription 按方案名称生成组卷说明。
func (h *Handler) suggestSpeakingPlanDescription(c *gin.Context) {
	var req speakingPlanSuggestRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if msg := normalizeSuggestInput(&req); msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	if !h.llm.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "LLM 未配置，无法智能生成说明")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"lang":  req.Lang,
		"level": req.Level,
		"name":  req.Name,
	})
	raw, err := h.llm.ChatJSON(
		c.Request.Context(),
		planDescriptionSystemPrompt(req.Lang, req.Level),
		"请为以下方案撰写组卷说明：\n"+string(payload),
		800,
	)
	if err != nil {
		h.logger.Printf("suggest speaking plan description llm: %v", err)
		writeError(c, http.StatusBadGateway, "方案说明生成失败")
		return
	}
	description, err := parsePlanDescription(raw)
	if err != nil {
		h.logger.Printf("suggest speaking plan description output rejected: %v", err)
		writeError(c, http.StatusBadGateway, "方案说明结果无效："+err.Error())
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"description": description})
}

func planNameSuggestSystemPrompt(lang, level string) string {
	return fmt.Sprintf(`你是语言口语考试的命题专家。用户只给出一个粗略主题，请把它具体化成 3 个互不相同的口语练习方案名称。

硬性要求：
1. 每个名称都是一个具体、可执行的场景，而不是主题的同义改写；场景之间要有明显差异（不同场合、不同交际目的）。
2. 名称使用中文，不超过 30 字，直接可作为方案名称。
3. 难度匹配语种 %s 的 CEFR %s 水平。
4. exclude 中列出的名称不要再次出现，也不要只是换种说法。
5. 只输出 JSON 对象：{"candidates":["...","...","..."]}，恰好 3 条。`, lang, level)
}

func planDescriptionSystemPrompt(lang, level string) string {
	return fmt.Sprintf(`你是语言口语考试的命题专家。请根据方案名称为该口语方案撰写组卷说明，供后续选题与补题使用。

硬性要求：
1. 说明使用中文，80–200 字。
2. 明确覆盖的话题场景、交际目标，以及希望训练的能力（如自我介绍、观点表达、双人互动）。
3. 指明考试部分组合：part1_intro（自我介绍与问答）、part2_presentation（主题陈述）、part3_discussion（双人讨论），可按名称含义取舍。
4. 难度匹配语种 %s 的 CEFR %s 水平，提示优选题库已有内容、缺失时补题。
5. 只输出 JSON 对象：{"description":"..."}。`, lang, level)
}

// parsePlanNameCandidates 提取候选名称，去重并限制长度；全部无效时报错。
func parsePlanNameCandidates(raw string) ([]string, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("返回内容不是 JSON 对象")
	}
	var reply struct {
		Candidates []string `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &reply); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	out := make([]string, 0, len(reply.Candidates))
	seen := make(map[string]bool, len(reply.Candidates))
	for _, item := range reply.Candidates {
		item = strings.TrimSpace(item)
		if item == "" || len([]rune(item)) > 100 || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未生成有效候选")
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out, nil
}

// parsePlanDescription 提取说明文本，超长时截断到 2000 字。
func parsePlanDescription(raw string) (string, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", fmt.Errorf("返回内容不是 JSON 对象")
	}
	var reply struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &reply); err != nil {
		return "", fmt.Errorf("JSON 解析失败: %w", err)
	}
	description := strings.TrimSpace(reply.Description)
	if description == "" {
		return "", fmt.Errorf("说明为空")
	}
	runes := []rune(description)
	if len(runes) > maxGeneratedPlanDescRunes {
		description = string(runes[:maxGeneratedPlanDescRunes])
	}
	return description, nil
}
