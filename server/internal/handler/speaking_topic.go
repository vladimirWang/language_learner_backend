package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	// 主题对话脚本缓存：同一主题在 TTL 内复用，避免重复调用 LLM。
	topicPlanTTL     = 10 * time.Minute
	defaultMinRounds = 5
	maxTopicRounds   = 10
)

// topicPlanReq 是「主题对话编排」请求：把一个话题下的细粒度语料串成多轮对话。
type topicPlanReq struct {
	Lang      string `json:"lang"`
	Level     string `json:"level"`
	Topic     string `json:"topic"`
	MinRounds int    `json:"min_rounds"`
}

// topicPlanRound 是主题对话中的一轮考官提问。
type topicPlanRound struct {
	Order          int      `json:"order"`
	QuestionDE     string   `json:"question_de"`
	QuestionZH     string   `json:"question_zh,omitempty"`
	ItemKey        string   `json:"item_key,omitempty"`
	ExpectedPoints []string `json:"expected_points"`
	RefAnswer      string   `json:"ref_answer,omitempty"`
	Kind           string   `json:"kind"` // anchor（素材题）/ bridge（衔接追问）
}

type topicPlanResp struct {
	Lang      string           `json:"lang"`
	Level     string           `json:"level"`
	Topic     string           `json:"topic"`
	MinRounds int              `json:"min_rounds"`
	Rounds    []topicPlanRound `json:"rounds"`
	Source    string           `json:"source"` // llm / template
}

// parsePointList 解析 speaking_items.expected_points（JSON 数组或分隔字符串）。
func parsePointList(raw string) []string {
	out := make([]string, 0)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		for _, p := range arr {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	for _, p := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == '/' || r == '\n'
	}) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// speakingTopicPlan 把一个主题下的语料题编排成至少 min_rounds 轮的完整对话。
// 语料不足以支撑轮次时，由 LLM 生成自然的衔接追问；LLM 不可用时回退模板。
func (h *Handler) speakingTopicPlan(c *gin.Context) {
	var req topicPlanReq
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Lang = strings.TrimSpace(strings.ToLower(req.Lang))
	if req.Lang == "" {
		req.Lang = "de"
	}
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.Topic = strings.TrimSpace(req.Topic)
	if !validCefrLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	if req.Topic == "" {
		writeError(c, http.StatusBadRequest, "topic is required")
		return
	}
	minRounds := req.MinRounds
	if minRounds <= 0 {
		minRounds = defaultMinRounds
	}
	if minRounds > maxTopicRounds {
		minRounds = maxTopicRounds
	}

	key := fmt.Sprintf("%s|%s|%s|%d", req.Lang, req.Level, req.Topic, minRounds)
	if plan, ok := h.cachedTopicPlan(key); ok {
		writeJSON(c, http.StatusOK, plan)
		return
	}

	items, err := h.store.ListSpeakingItemsByTopic(c.Request.Context(), req.Lang, req.Level, req.Topic)
	if err != nil {
		h.logger.Printf("topic plan items: %v", err)
		writeError(c, http.StatusInternalServerError, "load topic items failed")
		return
	}
	anchors := make([]topicPlanRound, 0, len(items))
	for _, item := range items {
		question := strings.TrimSpace(item.PromptDE)
		if question == "" {
			continue
		}
		anchors = append(anchors, topicPlanRound{
			QuestionDE:     question,
			QuestionZH:     item.PromptZH,
			ItemKey:        item.ItemKey,
			ExpectedPoints: parsePointList(item.ExpectedPoints),
			RefAnswer:      item.RefAnswer,
			Kind:           "anchor",
		})
	}
	if len(anchors) == 0 {
		writeError(c, http.StatusBadRequest, "topic has no active speaking items")
		return
	}

	target := len(anchors)
	if target < minRounds {
		target = minRounds
	}
	if target > maxTopicRounds {
		target = maxTopicRounds
	}

	plan := topicPlanResp{
		Lang:      req.Lang,
		Level:     req.Level,
		Topic:     req.Topic,
		MinRounds: minRounds,
		Source:    "template",
	}
	if len(anchors) < target && h.llm.Enabled() {
		raw, err := h.llm.ChatJSON(
			c.Request.Context(),
			topicPlanSystemPrompt(req.Level),
			topicPlanUserPrompt(req.Level, req.Topic, anchors, target),
			1000,
		)
		if err != nil {
			h.logger.Printf("topic plan llm: %v", err)
		} else if rounds, ok := parseTopicPlanReply(raw, req.Level, anchors, target); ok {
			plan.Rounds = rounds
			plan.Source = "llm"
		} else {
			h.logger.Printf("topic plan llm output rejected: %q", raw)
		}
	}
	if len(plan.Rounds) == 0 {
		plan.Rounds = fallbackTopicRounds(c.Request.Context(), h, anchors, target)
	}
	for i := range plan.Rounds {
		plan.Rounds[i].Order = i + 1
		if plan.Rounds[i].ExpectedPoints == nil {
			plan.Rounds[i].ExpectedPoints = []string{}
		}
	}
	h.storeTopicPlan(key, plan)
	writeJSON(c, http.StatusOK, plan)
}

func topicPlanSystemPrompt(level string) string {
	return fmt.Sprintf(`你是歌德学院 %s 级别口语考官，擅长把零散的问答素材组织成一段自然的主题对话。

必须做到：
1. 只使用 %s 等级范围内的词汇与句式，语法正确（冠词、格、介词支配、语序、变位）。
2. 每轮只输出一到两句德语提问，围绕同一主题，由上一轮自然引出。
3. 不评价、不讲解、不给分。
4. 不重复已出现过的问题。
5. 只输出 JSON，不要输出解释。`, level, level)
}

func topicPlanUserPrompt(level, topic string, anchors []topicPlanRound, target int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "主题：%s\n等级：%s\n目标轮次：%d\n\n素材问句（必须全部覆盖，并保持给定顺序作为对话骨架）：\n", topic, level, target)
	for i, a := range anchors {
		fmt.Fprintf(&sb, "[%d] %s\n", i, a.QuestionDE)
	}
	sb.WriteString("\n请补足素材之间的衔接与追问，组织成连贯的对话。")
	sb.WriteString("\n输出格式：{\"rounds\":[{\"question_de\":\"德语提问\",\"anchor_index\":0}]}")
	sb.WriteString("，anchor_index 为对应素材编号，新增追问填 -1。每轮一到两句，全部德语。只输出 JSON。")
	return sb.String()
}

type topicPlanReplyItem struct {
	QuestionDE  string `json:"question_de"`
	AnchorIndex int    `json:"anchor_index"`
}

type topicPlanReply struct {
	Rounds []topicPlanReplyItem `json:"rounds"`
}

// parseTopicPlanReply 校验 LLM 编排结果：轮次充足、语法合规、无重复且覆盖全部素材。
func parseTopicPlanReply(raw, level string, anchors []topicPlanRound, target int) ([]topicPlanRound, bool) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var reply topicPlanReply
	if err := json.Unmarshal([]byte(raw[start:end+1]), &reply); err != nil || len(reply.Rounds) < target {
		return nil, false
	}
	rounds := make([]topicPlanRound, 0, target)
	asked := make([]string, 0, target)
	covered := make(map[int]bool)
	for _, item := range reply.Rounds {
		if len(rounds) >= target {
			break
		}
		q := sanitizeReply(item.QuestionDE)
		if !validGermanReply(q, level) {
			return nil, false
		}
		if similarToAny(q, asked) {
			continue
		}
		round := topicPlanRound{QuestionDE: q, Kind: "bridge", ExpectedPoints: []string{}}
		if item.AnchorIndex >= 0 && item.AnchorIndex < len(anchors) && !covered[item.AnchorIndex] {
			anchor := anchors[item.AnchorIndex]
			round.QuestionDE = anchor.QuestionDE
			round.QuestionZH = anchor.QuestionZH
			round.ItemKey = anchor.ItemKey
			round.ExpectedPoints = anchor.ExpectedPoints
			round.RefAnswer = anchor.RefAnswer
			round.Kind = "anchor"
			covered[item.AnchorIndex] = true
		}
		asked = append(asked, round.QuestionDE)
		rounds = append(rounds, round)
	}
	if len(rounds) < target || len(covered) != len(anchors) {
		return nil, false
	}
	return rounds, true
}

// fallbackTopicRounds 按素材顺序铺开，再用兜底追问补足轮次。
func fallbackTopicRounds(ctx context.Context, h *Handler, anchors []topicPlanRound, target int) []topicPlanRound {
	rounds := make([]topicPlanRound, 0, target)
	asked := make([]string, 0, target)
	for _, anchor := range anchors {
		if len(rounds) >= target {
			break
		}
		rounds = append(rounds, anchor)
		asked = append(asked, anchor.QuestionDE)
	}
	pool := h.templatePool(ctx, "part1_intro", "examiner")
	for i := 0; len(rounds) < target && len(pool) > 0; i++ {
		q := pool[i%len(pool)]
		if similarToAny(q, asked) {
			if i > len(pool)*2 {
				break
			}
			continue
		}
		rounds = append(rounds, topicPlanRound{QuestionDE: q, Kind: "bridge", ExpectedPoints: []string{}})
		asked = append(asked, q)
	}
	for len(rounds) < target {
		rounds = append(rounds, topicPlanRound{
			QuestionDE:     "Können Sie das noch etwas genauer erklären?",
			Kind:           "bridge",
			ExpectedPoints: []string{},
		})
	}
	return rounds
}

func (h *Handler) cachedTopicPlan(key string) (topicPlanResp, bool) {
	h.planMu.RLock()
	defer h.planMu.RUnlock()
	if len(h.planCache) == 0 || time.Since(h.planAt) >= topicPlanTTL {
		return topicPlanResp{}, false
	}
	plan, ok := h.planCache[key]
	return plan, ok
}

func (h *Handler) storeTopicPlan(key string, plan topicPlanResp) {
	h.planMu.Lock()
	defer h.planMu.Unlock()
	if h.planCache == nil || time.Since(h.planAt) >= topicPlanTTL {
		h.planCache = make(map[string]topicPlanResp)
	}
	h.planCache[key] = plan
	h.planAt = time.Now()
}

// invalidateTopicPlanCache 在口语题库变更后清空编排缓存。
func (h *Handler) invalidateTopicPlanCache() {
	h.planMu.Lock()
	h.planCache = nil
	h.planAt = time.Time{}
	h.planMu.Unlock()
}

// validateItemReq 是管理台「把语料配置到主题下」时的校验请求。
type validateItemReq struct {
	Lang           string `json:"lang"`
	Level          string `json:"level"`
	Topic          string `json:"topic"`
	PromptDE       string `json:"prompt_de"`
	PromptZH       string `json:"prompt_zh"`
	ExpectedPoints string `json:"expected_points"`
	RefAnswer      string `json:"ref_answer"`
	ErrorNote      string `json:"error_note"`
}

type validateItemIssue struct {
	Text       string `json:"text"`
	Correction string `json:"correction"`
	Reason     string `json:"reason"`
}

type validateItemResp struct {
	OK        bool                `json:"ok"`
	GrammarOK bool                `json:"grammar_ok"`
	TopicFit  bool                `json:"topic_fit"`
	Issues    []validateItemIssue `json:"issues"`
	Reason    string              `json:"reason"`
	Source    string              `json:"source"` // llm / template / error
}

// validateSpeakingItem 用 LLM 校验语料的语法与主题归属，供管理台保存前调用。
func (h *Handler) validateSpeakingItem(c *gin.Context) {
	var req validateItemReq
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Lang = strings.TrimSpace(strings.ToLower(req.Lang))
	if req.Lang == "" {
		req.Lang = "de"
	}
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.Topic = strings.TrimSpace(req.Topic)
	req.PromptDE = strings.TrimSpace(req.PromptDE)
	req.RefAnswer = strings.TrimSpace(req.RefAnswer)
	if !validCefrLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	if req.Topic == "" || req.PromptDE == "" {
		writeError(c, http.StatusBadRequest, "topic and prompt_de are required")
		return
	}
	if utf8.RuneCountInString(req.PromptDE) > 300 || utf8.RuneCountInString(req.RefAnswer) > 600 {
		writeError(c, http.StatusBadRequest, "text is too long")
		return
	}

	if !h.llm.Enabled() {
		writeJSON(c, http.StatusOK, validateItemResp{
			OK: true, GrammarOK: true, TopicFit: true,
			Issues: []validateItemIssue{}, Source: "template",
			Reason: "LLM 未启用，已跳过校验。",
		})
		return
	}

	refs := h.grammarReferences(c.Request.Context(), req.Level, req.Topic, req.PromptDE, req.RefAnswer)
	raw, err := h.llm.ChatJSON(
		c.Request.Context(),
		validateItemSystemPrompt(req.Level),
		validateItemUserPrompt(req, refs),
		800,
	)
	if err != nil {
		h.logger.Printf("validate speaking item: %v", err)
		writeJSON(c, http.StatusOK, validateItemResp{
			Issues: []validateItemIssue{}, Source: "error",
			Reason: "校验服务暂时不可用，请稍后重试。",
		})
		return
	}
	resp, ok := parseValidateItemReply(raw)
	if !ok {
		h.logger.Printf("validate speaking item unparsable: %q", raw)
		writeJSON(c, http.StatusOK, validateItemResp{
			Issues: []validateItemIssue{}, Source: "error",
			Reason: "校验结果无法解析，请稍后重试。",
		})
		return
	}
	resp.Source = "llm"
	resp.OK = resp.GrammarOK && resp.TopicFit
	if resp.Reason == "" || !cjkRe.MatchString(resp.Reason) {
		if resp.OK {
			resp.Reason = "语法正确，且符合主题。"
		} else {
			resp.Reason = "存在问题，请按提示修改后再保存。"
		}
	}
	writeJSON(c, http.StatusOK, resp)
}

func validateItemSystemPrompt(level string) string {
	return fmt.Sprintf(`你是歌德学院 %s 级德语教材审校员，负责审核口语素材条目。`, level)
}

func validateItemUserPrompt(req validateItemReq, refs []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "主题：%s\n等级：%s\n", req.Topic, req.Level)
	fmt.Fprintf(&sb, "素材问句：%s\n", req.PromptDE)
	if req.PromptZH != "" {
		fmt.Fprintf(&sb, "中文提示：%s\n", req.PromptZH)
	}
	if req.RefAnswer != "" {
		fmt.Fprintf(&sb, "参考回答：%s\n", req.RefAnswer)
	}
	if req.ExpectedPoints != "" {
		fmt.Fprintf(&sb, "考察要点（中文，仅供理解，不要当德语检查）：%s\n", req.ExpectedPoints)
	}
	if len(refs) > 0 {
		sb.WriteString("\n语法规范参考：\n")
		for _, ref := range refs {
			fmt.Fprintf(&sb, "- %s\n", ref)
		}
	}
	sb.WriteString(`
请完成两件事：
1. 检查素材问句与参考回答的德语语法、拼写、冠词/格/介词支配/语序/动词变位，只报确定的问题。
2. 判断素材问句是否属于上述主题，且难度是否适合该等级。

输出 JSON：{"grammar_ok":true,"topic_fit":true,"issues":[{"text":"问题片段","correction":"正确写法","reason":"中文说明"}],"reason":"中文一句话结论"}
reason 与 issues.reason 用简体中文，text/correction 用德语。只输出 JSON。`)
	return sb.String()
}

func parseValidateItemReply(raw string) (validateItemResp, bool) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return validateItemResp{}, false
	}
	var resp validateItemResp
	if err := json.Unmarshal([]byte(raw[start:end+1]), &resp); err != nil {
		return validateItemResp{}, false
	}
	if resp.Issues == nil {
		resp.Issues = []validateItemIssue{}
	}
	if len(resp.Issues) > 5 {
		resp.Issues = resp.Issues[:5]
	}
	for i := range resp.Issues {
		issue := &resp.Issues[i]
		issue.Text = clipText(issue.Text, 120)
		issue.Correction = clipText(issue.Correction, 160)
		issue.Reason = clipText(issue.Reason, 160)
		if issue.Reason != "" && !cjkRe.MatchString(issue.Reason) {
			issue.Reason = "此处不符合标准德语，请参考修改后的写法。"
		}
	}
	return resp, true
}
