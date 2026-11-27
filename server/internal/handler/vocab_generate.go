package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// 词条智能生成：按语种与等级让模型出候选，服务端查库去重并复用管理台校验，
// 默认只返回预览，确认后再写入。去重必须在服务端完成，不能只依赖提示词。

const (
	vocabGenerateMaxCount     = 20
	vocabGenerateMaxRounds    = 3
	vocabGeneratePerRound     = 2
	vocabGenerateExcludeLimit = 400
)

type vocabGenerateRequest struct {
	Lang     string           `json:"lang"`
	Level    string           `json:"level"`
	Count    int              `json:"count"`
	Theme    string           `json:"theme"`
	WordType string           `json:"word_type"`
	Commit   bool             `json:"commit"`
	Items    []vocabCandidate `json:"items"` // 前端确认后的候选；提供时直接校验写入，不再调用模型
}

type vocabCandidate struct {
	Lang        string `json:"lang"`
	Lemma       string `json:"lemma"`
	Level       string `json:"level"`
	WordType    string `json:"word_type,omitempty"`
	Gender      string `json:"gender,omitempty"`
	Plural      string `json:"plural,omitempty"`
	Separable   string `json:"separable,omitempty"`
	TransIntr   string `json:"trans_intr,omitempty"`
	CoreCase    string `json:"core_case,omitempty"`
	Phonetic    string `json:"phonetic,omitempty"`
	Kana        string `json:"kana,omitempty"`
	Kanji       string `json:"kanji,omitempty"`
	Romaji      string `json:"romaji,omitempty"`
	ChineseMean string `json:"chinese_mean"`
	EnglishMean string `json:"english_mean,omitempty"`
	Example     string `json:"example,omitempty"`
}

type vocabGenerateSkipped struct {
	Lemma  string `json:"lemma"`
	Reason string `json:"reason"`
}

type vocabGenerateResponse struct {
	Candidates []vocabCandidate       `json:"candidates"`
	Skipped    []vocabGenerateSkipped `json:"skipped"`
	Inserted   int                    `json:"inserted"`
	Updated    int                    `json:"updated"`
	Rounds     int                    `json:"rounds"`
}

// generatedVocabItem 是模型返回的单条候选；separable 可能是布尔或字符串。
type generatedVocabItem struct {
	Lemma       string          `json:"lemma"`
	WordType    string          `json:"word_type"`
	ChineseMean string          `json:"chinese_mean"`
	EnglishMean string          `json:"english_mean"`
	Gender      string          `json:"gender"`
	Plural      string          `json:"plural"`
	Separable   json.RawMessage `json:"separable"`
	TransIntr   string          `json:"trans_intr"`
	CoreCase    string          `json:"core_case"`
	Phonetic    string          `json:"phonetic"`
	Kana        string          `json:"kana"`
	Kanji       string          `json:"kanji"`
	Romaji      string          `json:"romaji"`
	Example     string          `json:"example"`
}

// generateVocabWords 按等级生成词条候选；commit=true 时写入数据库。
func (h *Handler) generateVocabWords(c *gin.Context) {
	var req vocabGenerateRequest
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 4<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}

	req.Lang = strings.TrimSpace(req.Lang)
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !supportedVocabLangs[req.Lang] {
		writeError(c, http.StatusBadRequest, "暂不支持的语种（仅 de/en/ja）")
		return
	}
	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	if !validVocabLevels[req.Level] {
		writeError(c, http.StatusBadRequest, "无效等级（A1–C2 / N5–N1）")
		return
	}
	if !levelMatchesLang(req.Lang, req.Level) {
		writeError(c, http.StatusBadRequest, "德语/英语只支持 A1–C2，日语只支持 N5–N1")
		return
	}
	req.WordType = strings.ToLower(strings.TrimSpace(req.WordType))
	if alias, ok := wordTypeAliases[req.WordType]; ok {
		req.WordType = alias
	}
	if req.WordType != "" && !validVocabWordTypes[req.WordType] {
		writeError(c, http.StatusBadRequest, "无效词性（noun/verb/adj/adv/prep/particle）")
		return
	}
	req.Theme = strings.TrimSpace(req.Theme)
	if utf8.RuneCountInString(req.Theme) > 50 {
		writeError(c, http.StatusBadRequest, "主题不能超过 50 个字符")
		return
	}
	if req.Count <= 0 {
		req.Count = 10
	}
	if req.Count > vocabGenerateMaxCount {
		req.Count = vocabGenerateMaxCount
	}

	// 确认写入：前端回传候选时不再调用模型。
	if len(req.Items) > 0 {
		h.commitVocabCandidates(c, req)
		return
	}
	if !req.Commit && len(req.Items) == 0 && !h.llm.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "LLM 未配置，无法智能生成词条")
		return
	}

	existing, err := h.store.AllVocabLemmas(c.Request.Context(), req.Lang)
	if err != nil {
		h.logger.Printf("generate vocab: load lemmas lang=%s: %v", req.Lang, err)
		writeError(c, http.StatusInternalServerError, "读取词库失败")
		return
	}
	seen := make(map[string]bool, len(existing))
	for _, lemma := range existing {
		seen[lemmaKey(lemma)] = true
	}
	exclude := append([]string{}, existing...)

	accepted := make([]vocabCandidate, 0, req.Count)
	skipped := make([]vocabGenerateSkipped, 0, 8)
	rounds := 0
	for rounds < vocabGenerateMaxRounds && len(accepted) < req.Count {
		rounds++
		// ChatJSON 的 maxTokens 上限是 2000，单轮要太多条会被截断，因此每轮最多要 8 条。
		want := (req.Count - len(accepted)) * vocabGeneratePerRound
		if want > 8 {
			want = 8
		}
		if want < 4 {
			want = 4
		}
		items, err := h.askVocabCandidates(c, req, want, exclude)
		if err != nil {
			h.logger.Printf("generate vocab llm round=%d: %v", rounds, err)
			if len(accepted) == 0 {
				writeError(c, http.StatusBadGateway, "词条生成失败：模型返回内容不完整，请重试")
				return
			}
			break
		}
		progress := 0
		for _, item := range items {
			if len(accepted) >= req.Count {
				break
			}
			candidate, input, reason := normalizeGeneratedVocab(item, req)
			if reason != "" {
				skipped = append(skipped, vocabGenerateSkipped{Lemma: item.Lemma, Reason: reason})
				continue
			}
			key := lemmaKey(input.Lemma)
			if seen[key] {
				skipped = append(skipped, vocabGenerateSkipped{Lemma: input.Lemma, Reason: "词库中已存在"})
				continue
			}
			seen[key] = true
			accepted = append(accepted, candidate)
			exclude = append(exclude, input.Lemma)
			progress++
		}
		if progress == 0 && len(items) == 0 {
			break
		}
	}

	resp := vocabGenerateResponse{
		Candidates: accepted,
		Skipped:    skipped,
		Rounds:     rounds,
	}
	if req.Commit && len(accepted) > 0 {
		inserted, updated, err := h.store.UpsertVocabWords(c.Request.Context(), toVocabInputs(accepted, req))
		if err != nil {
			h.logger.Printf("generate vocab commit: %v", err)
			writeError(c, http.StatusInternalServerError, "写入词条失败")
			return
		}
		resp.Inserted = inserted
		resp.Updated = updated
	}
	writeJSON(c, http.StatusOK, resp)
}

// commitVocabCandidates 校验前端勾选的候选后批量写入。
func (h *Handler) commitVocabCandidates(c *gin.Context, req vocabGenerateRequest) {
	inputs := make([]store.VocabWordInput, 0, len(req.Items))
	skipped := make([]vocabGenerateSkipped, 0, 4)
	candidates := make([]vocabCandidate, 0, len(req.Items))
	for _, item := range req.Items {
		item.Lang = req.Lang
		item.Level = req.Level
		candidate, input, reason := candidateToInput(item, req)
		if reason != "" {
			skipped = append(skipped, vocabGenerateSkipped{Lemma: item.Lemma, Reason: reason})
			continue
		}
		inputs = append(inputs, input)
		candidates = append(candidates, candidate)
	}
	if len(inputs) == 0 {
		writeJSON(c, http.StatusOK, vocabGenerateResponse{
			Candidates: candidates,
			Skipped:    skipped,
		})
		return
	}
	inserted, updated, err := h.store.UpsertVocabWords(c.Request.Context(), inputs)
	if err != nil {
		h.logger.Printf("commit vocab candidates: %v", err)
		writeError(c, http.StatusInternalServerError, "写入词条失败")
		return
	}
	writeJSON(c, http.StatusOK, vocabGenerateResponse{
		Candidates: candidates,
		Skipped:    skipped,
		Inserted:   inserted,
		Updated:    updated,
	})
}

func (h *Handler) askVocabCandidates(
	c *gin.Context, req vocabGenerateRequest, want int, exclude []string,
) ([]generatedVocabItem, error) {
	if len(exclude) > vocabGenerateExcludeLimit {
		exclude = exclude[len(exclude)-vocabGenerateExcludeLimit:]
	}
	payload, _ := json.Marshal(map[string]any{
		"lang":      req.Lang,
		"level":     req.Level,
		"count":     want,
		"theme":     req.Theme,
		"word_type": req.WordType,
		"exclude":   exclude,
	})
	raw, err := h.llm.ChatJSON(
		c.Request.Context(),
		vocabGenerateSystemPrompt(req.Lang, req.Level),
		"请生成候选词条：\n"+string(payload),
		1800,
	)
	if err != nil {
		return nil, err
	}
	return parseGeneratedVocab(raw)
}

func vocabGenerateSystemPrompt(lang, level string) string {
	return fmt.Sprintf(`你是语言学习词库编辑。请为语种 %s、等级 %s 生成词条候选。

只输出 JSON 对象：{"items":[...]}，每项字段：
- lemma：词条原形（德语名词首字母大写；不要短语或变位形式）
- word_type：noun / verb / adj / adv / prep / particle 之一
- chinese_mean：中文释义（可带简短说明）
- english_mean：英文释义
- example：该语种的一句自然例句
- 德语名词必填 gender（m/f/n）与 plural；德语动词按需填 separable（true/false）
- 英语动词可填 trans_intr（vt/vi）与 core_case；英语可填 phonetic（国际音标）
- 日语填 kana、romaji，kanji 可选
- 德语/英语动词可填 core_case（如 +D、+A）

硬性要求：
1. 只生成该等级常见、实用的词；不要专有名词、人名、地名。
2. exclude 中已有的词不要再出现，也不要只换大小写或加复数。
3. 同一批内不要重复。
4. 不要输出解释、注释或代码块，只输出 JSON。`, lang, level)
}

// normalizeGeneratedVocab 把模型候选映射成管理台字段并复用统一校验。
func normalizeGeneratedVocab(item generatedVocabItem, req vocabGenerateRequest) (vocabCandidate, store.VocabWordInput, string) {
	candidate := vocabCandidate{
		Lang:        req.Lang,
		Lemma:       strings.TrimSpace(item.Lemma),
		Level:       req.Level,
		WordType:    strings.TrimSpace(item.WordType),
		ChineseMean: strings.TrimSpace(item.ChineseMean),
		EnglishMean: strings.TrimSpace(item.EnglishMean),
		Example:     strings.TrimSpace(item.Example),
	}
	switch req.Lang {
	case "de":
		candidate.Gender = strings.TrimSpace(item.Gender)
		candidate.Plural = strings.TrimSpace(item.Plural)
		candidate.Separable = separableText(item.Separable)
		candidate.TransIntr = strings.TrimSpace(item.TransIntr)
		candidate.CoreCase = strings.TrimSpace(item.CoreCase)
	case "en":
		candidate.Phonetic = strings.TrimSpace(item.Phonetic)
		candidate.TransIntr = strings.TrimSpace(item.TransIntr)
		candidate.CoreCase = strings.TrimSpace(item.CoreCase)
	case "ja":
		candidate.Kana = strings.TrimSpace(item.Kana)
		candidate.Kanji = strings.TrimSpace(item.Kanji)
		candidate.Romaji = strings.TrimSpace(item.Romaji)
	}
	return candidateToInput(candidate, req)
}

// candidateToInput 做字段校验与语种落列，返回规范化后的输入。
func candidateToInput(
	candidate vocabCandidate, req vocabGenerateRequest,
) (vocabCandidate, store.VocabWordInput, string) {
	upsert := vocabWordUpsert{
		Lang:        req.Lang,
		Lemma:       candidate.Lemma,
		Level:       req.Level,
		WordType:    candidate.WordType,
		Gender:      candidate.Gender,
		Plural:      candidate.Plural,
		Separable:   candidate.Separable,
		TransIntr:   candidate.TransIntr,
		CoreCase:    candidate.CoreCase,
		Phonetic:    candidate.Phonetic,
		Kana:        candidate.Kana,
		Kanji:       candidate.Kanji,
		Romaji:      candidate.Romaji,
		ChineseMean: candidate.ChineseMean,
		EnglishMean: candidate.EnglishMean,
	}
	switch req.Lang {
	case "de":
		upsert.ExampleDE = candidate.Example
	case "en":
		upsert.ExampleEN = candidate.Example
	case "ja":
		upsert.ExampleJA = candidate.Example
	}
	input, msg := normalizeVocabWord(upsert, req.Lang)
	if msg != "" {
		return vocabCandidate{}, store.VocabWordInput{}, msg
	}
	candidate.Lang = input.Lang
	candidate.Level = input.Level
	candidate.Lemma = input.Lemma
	candidate.WordType = input.WordType
	candidate.ChineseMean = input.ChineseMean
	return candidate, input, ""
}

func toVocabInputs(candidates []vocabCandidate, req vocabGenerateRequest) []store.VocabWordInput {
	out := make([]store.VocabWordInput, 0, len(candidates))
	for _, candidate := range candidates {
		_, input, reason := candidateToInput(candidate, req)
		if reason != "" {
			continue
		}
		out = append(out, input)
	}
	return out
}

func parseGeneratedVocab(raw string) ([]generatedVocabItem, error) {
	body := strings.TrimSpace(raw)
	// 去掉 ```json 围栏；被截断时可能没有收尾围栏。
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(body, "```")
	body = strings.TrimSpace(body)

	if items, ok := decodeVocabItems(body); ok {
		return items, nil
	}
	// 模型输出被 max_tokens 截断时，完整 JSON 无法解析；
	// 逐个提取已闭合的对象，尽量保留已生成的有效候选。
	if items := scanVocabItems(body); len(items) > 0 {
		return items, nil
	}
	return nil, fmt.Errorf("模型未返回可解析的词条 JSON（前 120 字：%s）", snippet(body, 120))
}

// decodeVocabItems 按完整 JSON 解析对象或数组。
func decodeVocabItems(body string) ([]generatedVocabItem, bool) {
	var payload struct {
		Items []generatedVocabItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err == nil {
		return payload.Items, true
	}
	var items []generatedVocabItem
	if err := json.Unmarshal([]byte(body), &items); err == nil {
		return items, true
	}
	return nil, false
}

// scanVocabItems 从（可能被截断的）文本里提取每个已闭合的词条对象。
func scanVocabItems(body string) []generatedVocabItem {
	if start := strings.Index(body, `"items"`); start >= 0 {
		if bracket := strings.Index(body[start:], "["); bracket >= 0 {
			body = body[start+bracket+1:]
		}
	}
	items := make([]generatedVocabItem, 0, 8)
	depth, objStart := 0, -1
	inString, escaped := false, false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				objStart = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && objStart >= 0 {
				var item generatedVocabItem
				if err := json.Unmarshal([]byte(body[objStart:i+1]), &item); err == nil &&
					strings.TrimSpace(item.Lemma) != "" {
					items = append(items, item)
				}
				objStart = -1
			}
		}
	}
	return items
}

func snippet(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// separableText 兼容模型返回布尔或字符串。
func separableText(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return ""
	}
	var boolValue bool
	if err := json.Unmarshal(raw, &boolValue); err == nil {
		if boolValue {
			return "true"
		}
		return "false"
	}
	var stringValue string
	if err := json.Unmarshal(raw, &stringValue); err == nil {
		return strings.TrimSpace(stringValue)
	}
	return ""
}

func lemmaKey(lemma string) string {
	return strings.ToLower(strings.TrimSpace(lemma))
}

func levelMatchesLang(lang, level string) bool {
	if lang == "ja" {
		return strings.HasPrefix(level, "N")
	}
	return !strings.HasPrefix(level, "N")
}
