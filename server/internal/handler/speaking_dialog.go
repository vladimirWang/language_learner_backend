package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

var ragKeywordRe = regexp.MustCompile(`[A-Za-zÄÖÜäöüß]{5,}`)

// speakingReferences 从题库问句与话题中抽取关键词，检索少量参考语料。
// 命中失败或没有语料时返回空，不影响追问生成。
func (h *Handler) speakingReferences(ctx context.Context, req speakingReplyReq) []string {
	if h.store == nil {
		return nil
	}
	seen := make(map[string]bool)
	keywords := make([]string, 0, 3)
	for _, word := range ragKeywordRe.FindAllString(req.Topic+" "+req.PromptDE, -1) {
		kw := strings.ToLower(word)
		if seen[kw] {
			continue
		}
		seen[kw] = true
		keywords = append(keywords, kw)
		if len(keywords) >= 3 {
			break
		}
	}
	refs, err := h.store.SearchRagChunks(ctx, "de", req.Level, keywords, 2)
	if err != nil {
		h.logger.Printf("speaking rag search: %v", err)
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.Join(strings.Fields(ref), " ")
		if utf8.RuneCountInString(ref) > 220 {
			ref = string([]rune(ref)[:220]) + "…"
		}
		if ref != "" {
			out = append(out, ref)
		}
	}
	return out
}

// grammarReferences 从语法库检索与当前对话相关的规范切片。
// 用于约束机器提问，以及作为批改考生回答的判定依据。
func (h *Handler) grammarReferences(ctx context.Context, level string, texts ...string) []string {
	if h.store == nil {
		return nil
	}
	seen := make(map[string]bool)
	keywords := make([]string, 0, 8)
	for _, word := range ragKeywordRe.FindAllString(strings.Join(texts, " "), -1) {
		kw := strings.ToLower(word)
		if seen[kw] {
			continue
		}
		seen[kw] = true
		keywords = append(keywords, kw)
		if len(keywords) >= 8 {
			break
		}
	}
	chunks, err := h.store.GrammarChunks(ctx, "de", level, keywords, 4)
	if err != nil {
		h.logger.Printf("speaking grammar refs: %v", err)
		return nil
	}
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, "【"+chunk.Title+"】"+chunk.Content)
	}
	return out
}

// speakingReplyReq 是「受控追问 / 搭档回应」的请求体。
type speakingReplyReq struct {
	Lang          string            `json:"lang"`
	Level         string            `json:"level"`
	ExamPart      string            `json:"exam_part"`
	Topic         string            `json:"topic"`
	Role          string            `json:"role"` // examiner / partner
	History       []speakingTurnReq `json:"history"`
	FollowupCount int               `json:"followup_count"`
	PromptDE      string            `json:"prompt_de"`
}

type speakingTurnReq struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type speakingReplyResp struct {
	Reply  string `json:"reply"`
	Source string `json:"source"` // llm / template
}

var cjkRe = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}]`)

// 各等级单句允许的最大词数，用于拦截 LLM 超纲长句。
var levelMaxWords = map[string]int{
	"A1": 12, "A2": 16, "B1": 22, "B2": 28, "C1": 32, "C2": 36,
}

// 追问模板：LLM 不可用或输出不合规时使用，保证无幻觉。
var examinerTemplates = map[string][]string{
	"part1_intro": {
		"Können Sie das bitte noch etwas genauer sagen?",
		"Und warum ist das für Sie wichtig?",
		"Wie oft machen Sie das?",
		"Haben Sie ein Beispiel dafür?",
		"Und wie war das früher bei Ihnen?",
		"Wer macht das bei Ihnen zu Hause?",
		"Was gefällt Ihnen daran am besten?",
		"Gibt es dabei auch etwas Schwieriges?",
		"Wie lange machen Sie das schon?",
		"Mit wem machen Sie das meistens?",
	},
	"part2_presentation": {
		"Was ist dabei der wichtigste Punkt für Sie?",
		"Können Sie dazu ein Beispiel geben?",
		"Welche Erfahrung haben Sie damit gemacht?",
		"Was würden Sie anderen in dieser Situation empfehlen?",
		"Gibt es dabei auch Nachteile?",
		"Wie hat sich das in den letzten Jahren verändert?",
		"Welche Rolle spielt das für Ihre Familie?",
		"Was könnte man in Zukunft besser machen?",
	},
	"part3_discussion": {
		"Das ist ein guter Punkt. Wie sehen Sie das?",
		"Können Sie das noch etwas erklären?",
		"Was würden Sie in dieser Situation machen?",
		"Ich sehe das etwas anders. Was meinen Sie dazu?",
		"Und wie wichtig ist Ihnen das persönlich?",
		"Was spricht aus Ihrer Sicht dagegen?",
		"Haben Sie das selbst schon erlebt?",
		"Wie würden Ihre Freunde darüber denken?",
	},
}

var partnerTemplates = []string{
	"Das sehe ich ähnlich. Aber ich finde, es gibt auch Nachteile.",
	"Interessant. Bei mir ist das ein bisschen anders.",
	"Guter Punkt. Was würden Sie in dieser Situation machen?",
	"Ich stimme Ihnen teilweise zu. Wie wichtig ist das für Sie?",
	"Das kann ich gut verstehen. Haben Sie ein Beispiel dafür?",
	"Da bin ich anderer Meinung. Wie würden Sie das begründen?",
	"Das überrascht mich. Wie kam es dazu?",
	"Bei uns ist das ähnlich. Was gefällt Ihnen daran am besten?",
	"Das finde ich spannend. Wie oft machen Sie das?",
	"Ehrlich gesagt sehe ich das anders. Warum ist das für Sie so wichtig?",
}

var wordSplitRe = regexp.MustCompile(`[a-zA-ZÄÖÜäöüß]{4,}`)

// contentWordSet 提取用于判重的实词集合（长度 ≥4 的词）。
func contentWordSet(text string) map[string]bool {
	set := make(map[string]bool)
	for _, word := range wordSplitRe.FindAllString(normalizeText(text), -1) {
		set[word] = true
	}
	return set
}

// similarToAny 判断候选句是否与既有句子重复。
// 先做归一化后的包含判定：短问句（如「Woher kommen Sie?」）
// 往往只有两个实词，且常被更长的问句完整包含，靠实词集合拦不住。
// 再用词集合相似度兜住「换汤不换药」的改写。
func similarToAny(candidate string, existing []string) bool {
	candNorm := normalizeText(candidate)
	if candNorm == "" {
		return false
	}
	for _, sentence := range existing {
		existNorm := normalizeText(sentence)
		if existNorm == "" {
			continue
		}
		if existNorm == candNorm {
			return true
		}
		shorter, longer := candNorm, existNorm
		if len(shorter) > len(longer) {
			shorter, longer = longer, shorter
		}
		// 太短的片段（如单个词）不做包含判定，避免误杀。
		if utf8.RuneCountInString(shorter) >= 8 && strings.Contains(longer, shorter) {
			return true
		}
	}
	candWords := contentWordSet(candidate)
	if len(candWords) < 3 {
		return false
	}
	for _, sentence := range existing {
		words := contentWordSet(sentence)
		if len(words) < 3 {
			continue
		}
		inter := 0
		for word := range candWords {
			if words[word] {
				inter++
			}
		}
		union := len(candWords) + len(words) - inter
		if union > 0 && float64(inter)/float64(union) >= 0.6 {
			return true
		}
		// 短问句的 Jaccard 容易偏低，包含度（交集 / 较小集合）≥0.75 也视为重复。
		smaller := len(candWords)
		if len(words) < smaller {
			smaller = len(words)
		}
		if float64(inter)/float64(smaller) >= 0.75 {
			return true
		}
	}
	return false
}

// normalizeText 去掉标点与多余空白并转小写，用于判重。
func normalizeText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer(
		".", "", "?", "", "!", "", ",", "", ";", "", ":", "",
		"\"", "", "'", "", "„", "", "“", "", "”", "",
	)
	return strings.Join(strings.Fields(replacer.Replace(text)), " ")
}

// pickTemplate 在模板池中轮转，跳过本轮对话里出现过（含近似重复）的句子。
func pickTemplate(pool []string, followupCount int, used map[string]bool, past []string) string {
	if len(pool) == 0 {
		return "Können Sie das noch etwas genauer erklären?"
	}
	start := followupCount % len(pool)
	for i := 0; i < len(pool); i++ {
		candidate := pool[(start+i)%len(pool)]
		if used[normalizeText(candidate)] {
			continue
		}
		if similarToAny(candidate, past) {
			continue
		}
		return candidate
	}
	return pool[start]
}

const templateCacheTTL = 60 * time.Second

// templatePool 从数据库读取兜底话术（60 秒缓存）；表为空或查询失败时回退代码内默认值。
func (h *Handler) templatePool(ctx context.Context, part, role string) []string {
	key := part
	if role == "partner" {
		key = "partner"
	}
	h.tplMu.RLock()
	pool := h.tplCache[key]
	loadedAt := h.tplCacheAt
	h.tplMu.RUnlock()
	if len(pool) > 0 && time.Since(loadedAt) < templateCacheTTL {
		return pool
	}
	if h.store != nil {
		items, err := h.store.ListSpeakingReplyTemplates(ctx, true)
		if err == nil {
			next := make(map[string][]string)
			for _, item := range items {
				if item.Lang != "" && item.Lang != "de" {
					continue
				}
				k := item.ExamPart
				if item.Role == "partner" {
					k = "partner"
				}
				next[k] = append(next[k], item.Text)
			}
			h.tplMu.Lock()
			h.tplCache = next
			h.tplCacheAt = time.Now()
			h.tplMu.Unlock()
			if len(next[key]) > 0 {
				return next[key]
			}
		} else {
			h.logger.Printf("load speaking templates: %v", err)
		}
	}
	return defaultTemplatePool(part, role)
}

func defaultTemplatePool(part, role string) []string {
	if role == "partner" {
		return partnerTemplates
	}
	if pool, ok := examinerTemplates[part]; ok && len(pool) > 0 {
		return pool
	}
	return examinerTemplates["part1_intro"]
}

// invalidateTemplateCache 在管理台增删改后清空缓存，下一次请求重新加载。
func (h *Handler) invalidateTemplateCache() {
	h.tplMu.Lock()
	h.tplCacheAt = time.Time{}
	h.tplMu.Unlock()
}

func examinerSystemPrompt(level, topic string) string {
	return fmt.Sprintf(`你是歌德学院 %s 级别口语考试的考官，与考生进行德语对话。

必须做到：
1. 只使用 %s 等级范围内的词汇与句式，禁止超纲；提问必须语法正确（冠词、格、介词支配、语序、动词变位）。
2. 每轮只输出一到两句德语提问，围绕话题 %s，不要跳到其他话题。
3. 不做语法讲解，只做自然的追问与引导。
4. 不评价对错、不打分。
5. 全程德语，不使用英语或中文。
6. 不要重复对话记录里已经问过的问题，每次追问都要换一个角度。
7. 只输出德语文本本身，不要加引号、翻译或解释。`, level, level, topic)
}

func partnerSystemPrompt(level, topic string) string {
	return fmt.Sprintf(`你是与考生同等级的 %s 德语考生，正在参加歌德口语第三部分的双人讨论，话题为 %s。

必须做到：
1. 以同级考生口吻表达观点，句子长度与词汇难度和 %s 一致。
2. 有观点、有互动：可以附和、补充或温和反驳，但不主导讨论。
3. 不纠正对方语法，不做讲解，不给分数。
4. 单轮输出不超过两句。
5. 不要重复对话记录里已经说过的话，每次回应都要有新观点。
6. 全程德语，只围绕 %s。只输出德语文本本身，不要加引号或解释。`, level, topic, level, topic)
}

// sanitizeReply 去掉引号、换行与多余空白的干扰。
func sanitizeReply(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "“”\"'`")
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.Join(strings.Fields(text), " ")
}

// validGermanReply 做轻量后置校验：命中异常则拒绝该输出并回退模板。
func validGermanReply(text, level string) bool {
	if text == "" {
		return false
	}
	if cjkRe.MatchString(text) {
		return false
	}
	n := utf8.RuneCountInString(text)
	if n < 5 || n > 240 {
		return false
	}
	// 句数不超过两句
	if strings.Count(text, ".")+strings.Count(text, "?")+strings.Count(text, "!") > 2 {
		return false
	}
	maxWords := levelMaxWords[level]
	if maxWords == 0 {
		maxWords = 22
	}
	words := strings.Fields(text)
	if len(words) > maxWords*2 {
		return false
	}
	for _, sentence := range regexp.MustCompile(`[.!?]+`).Split(text, -1) {
		fields := strings.Fields(sentence)
		if len(fields) > maxWords {
			return false
		}
	}
	return true
}

// speakingReply 生成追问或搭档回应：优先 LLM，异常时回退模板。
func (h *Handler) speakingReply(c *gin.Context) {
	var req speakingReplyReq
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.ExamPart = strings.TrimSpace(req.ExamPart)
	req.Topic = strings.TrimSpace(req.Topic)
	req.Role = strings.TrimSpace(req.Role)
	if !validCefrLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	if !validExamParts[req.ExamPart] {
		writeError(c, http.StatusBadRequest, "invalid exam_part")
		return
	}
	if req.Role != "partner" {
		req.Role = "examiner"
	}
	if req.FollowupCount < 0 {
		req.FollowupCount = 0
	}

	// 已出现过的句子：用于避免重复提问（含当前题库问句）。
	used := map[string]bool{}
	if p := normalizeText(req.PromptDE); p != "" {
		used[p] = true
	}
	for _, turn := range req.History {
		if text := normalizeText(turn.Text); text != "" {
			used[text] = true
		}
	}

	// 已出现过的问句（含题库当前问句），用于拦截重复提问。
	// 注意：只依赖调用方传入的完整历史，历史被截断会直接导致判重失效。
	past := make([]string, 0, len(req.History)+1)
	if strings.TrimSpace(req.PromptDE) != "" {
		past = append(past, req.PromptDE)
	}
	for _, turn := range req.History {
		if turn.Role != "user" && strings.TrimSpace(turn.Text) != "" {
			past = append(past, turn.Text)
		}
	}

	fallback := pickTemplate(h.templatePool(c.Request.Context(), req.ExamPart, req.Role), req.FollowupCount, used, past)

	if !h.llm.Enabled() {
		writeJSON(c, http.StatusOK, speakingReplyResp{Reply: fallback, Source: "template"})
		return
	}

	system := examinerSystemPrompt(req.Level, req.Topic)
	if req.Role == "partner" {
		system = partnerSystemPrompt(req.Level, req.Topic)
	}
	var sb strings.Builder
	sb.WriteString("题库当前话题：")
	sb.WriteString(req.Topic)
	sb.WriteString("\n当前考官问句：")
	sb.WriteString(req.PromptDE)
	sb.WriteString("\n对话记录：\n")
	for _, turn := range req.History {
		role := "考生"
		if turn.Role != "user" {
			role = "考官"
		}
		sb.WriteString(role)
		sb.WriteString("：")
		sb.WriteString(strings.TrimSpace(turn.Text))
		sb.WriteString("\n")
	}
	if req.Role == "partner" {
		sb.WriteString("\n请生成搭档对考生最后一句话的回应（德语，一到两句）。")
	} else {
		sb.WriteString("\n请生成考官对考生最后一句话的追问（德语，一到两句）。")
	}
	if refs := h.speakingReferences(c.Request.Context(), req); len(refs) > 0 {
		sb.WriteString("\n\n参考资料（仅可借鉴表达，不要照抄）：\n")
		for _, ref := range refs {
			sb.WriteString("- ")
			sb.WriteString(ref)
			sb.WriteString("\n")
		}
	}
	if refs := h.grammarReferences(c.Request.Context(), req.Level, req.Topic, req.PromptDE); len(refs) > 0 {
		sb.WriteString("\n\n语法规范参考（你的提问必须符合，不得出现违反这些规范的表达）：\n")
		for _, ref := range refs {
			sb.WriteString("- ")
			sb.WriteString(ref)
			sb.WriteString("\n")
		}
	}

	basePrompt := sb.String()
	reply, err := h.llm.Chat(c.Request.Context(), system, basePrompt)
	if err == nil {
		reply = sanitizeReply(reply)
		if validGermanReply(reply, req.Level) && !similarToAny(reply, past) {
			writeJSON(c, http.StatusOK, speakingReplyResp{Reply: reply, Source: "llm"})
			return
		}
		if err == nil && validGermanReply(reply, req.Level) {
			h.logger.Printf("speaking llm repeated a question, retrying: %q", reply)
			// 带「禁止重复」清单再请求一次，仍重复则回退模板。
			var ban strings.Builder
			for i := len(past) - 1; i >= 0 && ban.Len() < 600; i-- {
				ban.WriteString("- ")
				ban.WriteString(past[i])
				ban.WriteString("\n")
			}
			retry, retryErr := h.llm.Chat(c.Request.Context(), system,
				basePrompt+"\n\n以下句子已经问过，严禁重复或改写：\n"+ban.String()+
					"\n请换一个完全不同的角度提问。")
			if retryErr == nil {
				retry = sanitizeReply(retry)
				if validGermanReply(retry, req.Level) && !similarToAny(retry, past) {
					writeJSON(c, http.StatusOK, speakingReplyResp{Reply: retry, Source: "llm"})
					return
				}
			}
		} else {
			h.logger.Printf("speaking llm output rejected: %q", reply)
		}
	} else {
		h.logger.Printf("speaking llm: %v", err)
	}
	writeJSON(c, http.StatusOK, speakingReplyResp{Reply: fallback, Source: "template"})
}

// ---------------------------------------------------------------------------
// 语法批改（口语回答）
// ---------------------------------------------------------------------------

type speakingCheckReq struct {
	Lang           string `json:"lang"`
	Level          string `json:"level"`
	Topic          string `json:"topic"`
	PromptDE       string `json:"prompt_de"`
	Answer         string `json:"answer"`
	ExpectedPoints string `json:"expected_points"`
}

type grammarIssue struct {
	Type        string `json:"type"`        // 冠词 / 格 / 介词 / 语序 / 动词变位 / 搭配
	Snippet     string `json:"snippet"`     // 原句中的错误片段
	Fix         string `json:"fix"`         // 正确写法
	Explanation string `json:"explanation"` // 中文解释
}

type speakingCheckResp struct {
	Corrected string         `json:"corrected"`
	Errors    []grammarIssue `json:"errors"`
	Rules     []string       `json:"rules"`
	Comment   string         `json:"comment"`
	Source    string         `json:"source"` // llm / template
}

func grammarCheckSystemPrompt() string {
	return `你是歌德学院德语口语考试的语法批改老师，负责检查考生回答的语法、用词与固定搭配。

必须做到：
1. 只依据「语法规范参考」和标准德语语法判断，规范没有覆盖的条目不得臆造规则。
2. 只检查语法与搭配，不评价内容好坏，不打分。
3. 输出严格为 JSON，不要使用代码块，不要输出 JSON 以外的任何文字。
4. JSON 结构：
{"corrected":"语法正确的改写（尽量保留原意与原句结构）","errors":[{"type":"冠词/格/介词/语序/动词变位/搭配/其他","snippet":"原句中的错误片段","fix":"正确写法","explanation":"中文简短解释"}],"rules":["引用的规范标题"],"comment":"一句中文总评"}
5. type、explanation 与 comment 必须使用简体中文；corrected、snippet、fix 使用德语。
6. 没有语法错误时 errors 为空数组，corrected 可与原句相同，comment 说明表达正确。
7. 最多列出 4 处错误；考生回答过短或与题目无关时只在 comment 里提示，不要编造错误。

示例（只示范格式与语言，不要照抄内容）：
输入：Ich komme aus Thailand, bin nicht Spanien.
输出：{"corrected":"Ich komme aus Thailand, aber ich bin nicht aus Spanien.","errors":[{"type":"搭配","snippet":"bin nicht Spanien","fix":"aber ich bin nicht aus Spanien","explanation":"表示来自某地要用 aus + 第三格，sein 不能直接接国名。"}],"rules":["只跟第三格的介词"],"comment":"漏掉了介词 aus，两个分句之间缺少连词 aber。"}`
}

func grammarCheckUserPrompt(req speakingCheckReq, refs []string) string {
	var sb strings.Builder
	sb.WriteString("等级：")
	sb.WriteString(req.Level)
	sb.WriteString("\n话题：")
	sb.WriteString(req.Topic)
	sb.WriteString("\n考官问题：")
	sb.WriteString(req.PromptDE)
	sb.WriteString("\n考生回答：")
	sb.WriteString(req.Answer)
	if req.ExpectedPoints != "" {
		sb.WriteString("\n本题应覆盖的要点：")
		sb.WriteString(req.ExpectedPoints)
	}
	if len(refs) > 0 {
		sb.WriteString("\n\n语法规范参考：\n")
		for _, ref := range refs {
			sb.WriteString("- ")
			sb.WriteString(ref)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n注意：type、explanation、comment 必须用简体中文书写；corrected、snippet、fix 用德语。请只输出 JSON。")
	return sb.String()
}

// clipText 按字符数截断，避免 LLM 输出过长撑爆前端。
func clipText(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return text
	}
	return string([]rune(text)[:max]) + "…"
}

func parseGrammarCheckReply(raw string) (speakingCheckResp, bool) {
	resp := speakingCheckResp{Errors: []grammarIssue{}, Rules: []string{}}
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return resp, false
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &resp); err != nil {
		return resp, false
	}
	resp.Corrected = clipText(resp.Corrected, 300)
	resp.Comment = clipText(resp.Comment, 200)
	if resp.Errors == nil {
		resp.Errors = []grammarIssue{}
	}
	if len(resp.Errors) > 4 {
		resp.Errors = resp.Errors[:4]
	}
	for i := range resp.Errors {
		item := &resp.Errors[i]
		item.Type = clipText(item.Type, 20)
		item.Snippet = clipText(item.Snippet, 120)
		item.Fix = clipText(item.Fix, 120)
		item.Explanation = clipText(item.Explanation, 160)
		// 模型偶尔会用德语解释；没有中文时退回通用中文提示，避免前端出现纯德语说明。
		if item.Explanation != "" && !cjkRe.MatchString(item.Explanation) {
			item.Explanation = "此处不符合标准德语语法，请参考修改后的写法。"
		}
	}
	if resp.Rules == nil {
		resp.Rules = []string{}
	}
	if len(resp.Rules) > 4 {
		resp.Rules = resp.Rules[:4]
	}
	for i := range resp.Rules {
		// 模型可能沿用提示词里的「【文档名 · 小节】」格式，展示时去掉方括号。
		resp.Rules[i] = clipText(strings.Trim(resp.Rules[i], "【】 "), 60)
	}
	return resp, true
}

// speakingCheck 依据语法规范批改考生回答；LLM 不可用时返回空结果，由前端降级。
func (h *Handler) speakingCheck(c *gin.Context) {
	var req speakingCheckReq
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	req.Topic = strings.TrimSpace(req.Topic)
	req.PromptDE = strings.TrimSpace(req.PromptDE)
	req.Answer = strings.TrimSpace(req.Answer)
	req.ExpectedPoints = strings.TrimSpace(req.ExpectedPoints)
	if !validCefrLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	if req.Answer == "" {
		writeError(c, http.StatusBadRequest, "answer is required")
		return
	}
	if utf8.RuneCountInString(req.Answer) > 600 {
		writeError(c, http.StatusBadRequest, "answer is too long")
		return
	}

	empty := speakingCheckResp{Errors: []grammarIssue{}, Rules: []string{}, Source: "template"}
	if !h.llm.Enabled() {
		writeJSON(c, http.StatusOK, empty)
		return
	}
	refs := h.grammarReferences(c.Request.Context(), req.Level, req.Topic, req.PromptDE, req.Answer, req.ExpectedPoints)
	raw, err := h.llm.ChatJSON(c.Request.Context(), grammarCheckSystemPrompt(), grammarCheckUserPrompt(req, refs), 800)
	if err != nil {
		h.logger.Printf("speaking grammar check: %v", err)
		writeJSON(c, http.StatusOK, empty)
		return
	}
	resp, ok := parseGrammarCheckReply(raw)
	if !ok {
		h.logger.Printf("speaking grammar check unparsable: %q", raw)
		writeJSON(c, http.StatusOK, empty)
		return
	}
	resp.Source = "llm"
	if resp.Comment == "" || !cjkRe.MatchString(resp.Comment) {
		if len(resp.Errors) == 0 {
			resp.Comment = "语法正确，表达自然。"
		} else {
			resp.Comment = "存在语法或搭配问题，请参考上面的修改建议。"
		}
	}
	writeJSON(c, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// 兜底话术管理（管理台）
// ---------------------------------------------------------------------------

type speakingTemplateReq struct {
	ExamPart  string `json:"exam_part"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	Active    *bool  `json:"active"`
	SortOrder int    `json:"sort_order"`
}

func (req speakingTemplateReq) validate() (store.SpeakingReplyTemplate, error) {
	item := store.SpeakingReplyTemplate{}
	if !validExamParts[strings.TrimSpace(req.ExamPart)] {
		return item, errors.New("invalid exam_part")
	}
	role := strings.TrimSpace(req.Role)
	if role != "examiner" && role != "partner" {
		return item, errors.New("invalid role")
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return item, errors.New("text is required")
	}
	if utf8.RuneCountInString(text) > 300 {
		return item, errors.New("text is too long")
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	return store.SpeakingReplyTemplate{
		Lang:      "de",
		ExamPart:  strings.TrimSpace(req.ExamPart),
		Role:      role,
		Text:      text,
		Active:    active,
		SortOrder: req.SortOrder,
	}, nil
}

func (h *Handler) listSpeakingTemplates(c *gin.Context) {
	activeOnly := c.Query("active_only") == "true"
	items, err := h.store.ListSpeakingReplyTemplates(c.Request.Context(), activeOnly)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"templates": items})
}

func (h *Handler) createSpeakingTemplate(c *gin.Context) {
	var req speakingTemplateReq
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	item, err := req.validate()
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.CreateSpeakingReplyTemplate(c.Request.Context(), &item); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateTemplateCache()
	writeJSON(c, http.StatusOK, map[string]any{"id": item.ID})
}

func (h *Handler) updateSpeakingTemplate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	var req speakingTemplateReq
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	item, err := req.validate()
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	item.ID = id
	// 未传 active 时保留原状态，避免误把停用话术重新启用。
	if req.Active == nil {
		existing, getErr := h.store.GetSpeakingReplyTemplate(c.Request.Context(), id)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				writeError(c, http.StatusNotFound, "template not found")
				return
			}
			writeError(c, http.StatusInternalServerError, getErr.Error())
			return
		}
		item.Active = existing.Active
	}
	if err := h.store.UpdateSpeakingReplyTemplate(c.Request.Context(), &item); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "template not found")
			return
		}
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateTemplateCache()
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) deleteSpeakingTemplate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteSpeakingReplyTemplate(c.Request.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "template not found")
			return
		}
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateTemplateCache()
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}
