package handler

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// validVocabWordTypes 与 builder 各语种插件的 word_types 保持一致，保证管理台录入的词可回写 CSV。
var validVocabWordTypes = map[string]bool{
	"noun": true, "verb": true, "adj": true,
	"adv": true, "prep": true, "particle": true,
}

// wordTypeAliases 把常见英文完整词归一化为 builder 内部用的短代码。
// CSV 导入和单条录入都会先做这个归一化。
var wordTypeAliases = map[string]string{
	"adjective":    "adj",
	"adverbs":      "adv",
	"adverb":       "adv",
	"preposition":  "prep",
	"prepositions": "prep",
	"particles":    "particle",
	"nouns":        "noun",
	"verbs":        "verb",
}

// validVocabLevels 覆盖 CEFR（de/en）与 JLPT（ja）。
var validVocabLevels = map[string]bool{
	"A1": true, "A2": true, "B1": true, "B2": true, "C1": true, "C2": true,
	"N1": true, "N2": true, "N3": true, "N4": true, "N5": true,
}

// supportedVocabLangs 是已拆表的语种；其余语种没有对应词表。
var supportedVocabLangs = map[string]bool{"de": true, "en": true, "ja": true}

// vocabWordUpsert 是新增/更新词条的请求体。
type vocabWordUpsert struct {
	Lang         string `json:"lang"`
	Lemma        string `json:"lemma"`
	Level        string `json:"level"`
	WordType     string `json:"word_type"`
	Gender       string `json:"gender"`
	Plural       string `json:"plural"`
	Phonetic     string `json:"phonetic"`
	Kana         string `json:"kana"`
	Kanji        string `json:"kanji"`
	Romaji       string `json:"romaji"`
	Separable    string `json:"separable"`
	TransIntr    string `json:"trans_intr"`
	CoreCase     string `json:"core_case"`
	SortKey      string `json:"sort_key"`
	ExtraJSON    string `json:"extra_json"`
	ChineseMean  string `json:"chinese_mean"`
	EnglishMean  string `json:"english_mean"`
	ExampleDE    string `json:"example_de"`
	ExampleEN    string `json:"example_en"`
	ExampleJA    string `json:"example_ja"`
	SourceMDPath string `json:"source_md_path"`
}

// normalizeVocabWord 校验并规范化词条字段；返回中文错误描述。
func normalizeVocabWord(req vocabWordUpsert, forcedLang string) (store.VocabWordInput, string) {
	req.Lang = strings.TrimSpace(req.Lang)
	if req.Lang == "" {
		req.Lang = forcedLang
	}
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) {
		return store.VocabWordInput{}, "invalid lang"
	}
	if forcedLang != "" && req.Lang != forcedLang {
		return store.VocabWordInput{}, "lang 与所选语种不一致"
	}
	if !supportedVocabLangs[req.Lang] {
		return store.VocabWordInput{}, "暂不支持的语种（仅 de/en/ja）"
	}

	req.Lemma = strings.TrimSpace(req.Lemma)
	if req.Lemma == "" {
		return store.VocabWordInput{}, "词条（lemma）不能为空"
	}
	if utf8.RuneCountInString(req.Lemma) > 80 {
		return store.VocabWordInput{}, "词条（lemma）不能超过 80 个字符"
	}

	req.Level = strings.ToUpper(strings.TrimSpace(req.Level))
	if !validVocabLevels[req.Level] {
		return store.VocabWordInput{}, "无效等级（A1–C2 / N5–N1）"
	}

	req.WordType = strings.ToLower(strings.TrimSpace(req.WordType))
	if alias, ok := wordTypeAliases[req.WordType]; ok {
		req.WordType = alias
	}
	if !validVocabWordTypes[req.WordType] {
		return store.VocabWordInput{}, "无效词性（noun/verb/adj/adv/prep/particle）"
	}

	req.Gender = strings.ToLower(strings.TrimSpace(req.Gender))
	req.Plural = strings.TrimSpace(req.Plural)
	req.Phonetic = strings.TrimSpace(req.Phonetic)
	req.Kana = strings.TrimSpace(req.Kana)
	req.Kanji = strings.TrimSpace(req.Kanji)
	req.Romaji = strings.TrimSpace(req.Romaji)
	req.ExampleDE = strings.TrimSpace(req.ExampleDE)
	req.ExampleEN = strings.TrimSpace(req.ExampleEN)
	req.ExampleJA = strings.TrimSpace(req.ExampleJA)
	req.Separable = strings.ToLower(strings.TrimSpace(req.Separable))
	req.TransIntr = strings.ToLower(strings.TrimSpace(req.TransIntr))
	req.CoreCase = strings.TrimSpace(req.CoreCase)
	req.SortKey = strings.TrimSpace(req.SortKey)
	req.ExtraJSON = strings.TrimSpace(req.ExtraJSON)

	// 语种专有字段不允许跨语种填写，避免拆表后仍有脏数据。
	if req.Lang != "de" {
		if req.Gender != "" {
			return store.VocabWordInput{}, "只有德语词条可以填写词性性别"
		}
		if req.Plural != "" {
			return store.VocabWordInput{}, "只有德语词条可以填写复数"
		}
		if req.ExampleDE != "" {
			return store.VocabWordInput{}, "只有德语词条可以填写德语例句"
		}
	}
	if req.Lang != "en" {
		if req.Phonetic != "" {
			return store.VocabWordInput{}, "只有英语词条可以填写音标"
		}
		if req.ExampleEN != "" {
			return store.VocabWordInput{}, "只有英语词条可以填写英语例句"
		}
	}
	if req.Lang != "ja" {
		if req.Kana != "" || req.Kanji != "" || req.Romaji != "" {
			return store.VocabWordInput{}, "只有日语词条可以填写假名/汉字/罗马字"
		}
		if req.ExampleJA != "" {
			return store.VocabWordInput{}, "只有日语词条可以填写日语例句"
		}
	}
	// 语料字段（原先只存在于 vocab.csv）同样按语种约束。
	if req.Lang != "de" && req.Separable != "" {
		return store.VocabWordInput{}, "只有德语词条可以填写可分动词"
	}
	if req.Lang == "ja" && (req.TransIntr != "" || req.CoreCase != "") {
		return store.VocabWordInput{}, "日语词条不支持及物性/支配格"
	}
	switch req.Separable {
	case "", "true", "false":
	case "1", "yes", "y":
		req.Separable = "true"
	case "0", "no", "n":
		req.Separable = "false"
	default:
		return store.VocabWordInput{}, "separable 只能是 true 或 false"
	}
	if req.ExtraJSON != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(req.ExtraJSON), &obj); err != nil {
			return store.VocabWordInput{}, "extra_json 必须是 JSON 对象"
		}
	}
	if req.Gender != "" && req.Gender != "m" && req.Gender != "f" && req.Gender != "n" {
		return store.VocabWordInput{}, "无效词性性别（m/f/n）"
	}
	if req.WordType == "noun" && req.Lang == "de" && req.Gender == "" {
		return store.VocabWordInput{}, "德语名词必须填写词性性别（m/f/n）"
	}
	if req.WordType != "noun" && req.Gender != "" {
		return store.VocabWordInput{}, "只有名词可以填写词性性别"
	}

	req.ChineseMean = strings.TrimSpace(req.ChineseMean)
	if req.ChineseMean == "" {
		return store.VocabWordInput{}, "中文释义不能为空"
	}
	if utf8.RuneCountInString(req.ChineseMean) > 200 {
		return store.VocabWordInput{}, "中文释义不能超过 200 个字符"
	}

	req.EnglishMean = strings.TrimSpace(req.EnglishMean)
	req.SourceMDPath = strings.TrimSpace(req.SourceMDPath)
	if utf8.RuneCountInString(req.EnglishMean) > 200 || utf8.RuneCountInString(req.SourceMDPath) > 300 ||
		utf8.RuneCountInString(req.Plural) > 80 || utf8.RuneCountInString(req.Phonetic) > 80 ||
		utf8.RuneCountInString(req.Kana) > 80 || utf8.RuneCountInString(req.Kanji) > 80 ||
		utf8.RuneCountInString(req.Romaji) > 80 ||
		utf8.RuneCountInString(req.ExampleDE) > 500 || utf8.RuneCountInString(req.ExampleEN) > 500 ||
		utf8.RuneCountInString(req.ExampleJA) > 500 ||
		utf8.RuneCountInString(req.TransIntr) > 20 || utf8.RuneCountInString(req.CoreCase) > 80 ||
		utf8.RuneCountInString(req.SortKey) > 80 || utf8.RuneCountInString(req.ExtraJSON) > 2000 {
		return store.VocabWordInput{}, "字段长度超出限制"
	}

	return store.VocabWordInput{
		Lang:         req.Lang,
		Lemma:        req.Lemma,
		Level:        req.Level,
		WordType:     req.WordType,
		Gender:       req.Gender,
		Plural:       req.Plural,
		Phonetic:     req.Phonetic,
		Kana:         req.Kana,
		Kanji:        req.Kanji,
		Romaji:       req.Romaji,
		Separable:    req.Separable,
		TransIntr:    req.TransIntr,
		CoreCase:     req.CoreCase,
		SortKey:      req.SortKey,
		ExtraJSON:    req.ExtraJSON,
		ChineseMean:  req.ChineseMean,
		EnglishMean:  req.EnglishMean,
		ExampleDE:    req.ExampleDE,
		ExampleEN:    req.ExampleEN,
		ExampleJA:    req.ExampleJA,
		SourceMDPath: req.SourceMDPath,
	}, ""
}

// listVocabWords 返回新词库列表。支持 ?lang=&level=&q=&limit=&offset=。
func (h *Handler) listVocabWords(c *gin.Context) {
	lang := strings.TrimSpace(c.Query("lang"))
	if lang != "" && (!langPattern.MatchString(lang) || !supportedVocabLangs[lang]) {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	level := strings.ToUpper(strings.TrimSpace(c.Query("level")))
	if level != "" && !validVocabLevels[level] {
		writeError(c, http.StatusBadRequest, "invalid level")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		offset = 0
	}

	words, total, err := h.store.ListVocabWords(c.Request.Context(), lang, level, strings.TrimSpace(c.Query("q")), limit, offset)
	if err != nil {
		h.logger.Printf("list vocab words lang=%s: %v", lang, err)
		writeError(c, http.StatusInternalServerError, "list vocab words failed")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"words":  words,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// createVocabWord 新增词条；(lang, lemma) 已存在时返回 409。
func (h *Handler) createVocabWord(c *gin.Context) {
	var req vocabWordUpsert
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	input, msg := normalizeVocabWord(req, "")
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	id, created, err := h.store.CreateVocabWord(c.Request.Context(), input)
	if err != nil {
		h.logger.Printf("create vocab word %s/%s: %v", input.Lang, input.Lemma, err)
		writeError(c, http.StatusInternalServerError, "create vocab word failed")
		return
	}
	if !created {
		writeError(c, http.StatusConflict, "该词条已存在，请直接编辑")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"word_id": id})
}

// updateVocabWord 按 ID 更新词条。
func (h *Handler) updateVocabWord(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid word id")
		return
	}
	var req vocabWordUpsert
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	input, msg := normalizeVocabWord(req, "")
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	ok, err := h.store.UpdateVocabWord(c.Request.Context(), id, input)
	if err != nil {
		h.logger.Printf("update vocab word %d: %v", id, err)
		writeError(c, http.StatusConflict, "保存失败：同语种下该词条已存在")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "词条不存在")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// deleteVocabWord 按 ID 删除词条；lang 指明所在语种表。
func (h *Handler) deleteVocabWord(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c, http.StatusBadRequest, "invalid word id")
		return
	}
	lang := strings.TrimSpace(c.Query("lang"))
	if lang != "" && !supportedVocabLangs[lang] {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}
	ok, err := h.store.DeleteVocabWord(c.Request.Context(), lang, id)
	if err != nil {
		h.logger.Printf("delete vocab word %d: %v", id, err)
		writeError(c, http.StatusInternalServerError, "delete vocab word failed")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "词条不存在")
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"ok": true})
}

// importVocabWords 导入 CSV 文本；任一行校验失败时整批不写入。
// 可选同时回填学习记录（learned_date），device_id 固定为 "admin"。
func (h *Handler) importVocabWords(c *gin.Context) {
	var req struct {
		Lang        string `json:"lang"`
		CSV         string `json:"csv"`
		DryRun      bool   `json:"dry_run"`
		TagLearned  bool   `json:"tag_learned"`
		LearnedDate string `json:"learned_date"` // YYYY-MM-DD
	}
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 8<<20)).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json body")
		return
	}
	req.Lang = strings.TrimSpace(req.Lang)
	if req.Lang == "" {
		req.Lang = "de"
	}
	if !langPattern.MatchString(req.Lang) || !supportedVocabLangs[req.Lang] {
		writeError(c, http.StatusBadRequest, "invalid lang")
		return
	}

	// 学习记录标记：learned_date 必填，device_id 由后端固定为 "admin"。
	tagLearned := req.TagLearned
	if tagLearned {
		req.LearnedDate = strings.TrimSpace(req.LearnedDate)
		if req.LearnedDate == "" {
			writeError(c, http.StatusBadRequest, "标记学习记录时必须提供 learned_date（YYYY-MM-DD）")
			return
		}
		loc := h.now().Location()
		parsed, err := time.ParseInLocation("2006-01-02", req.LearnedDate, loc)
		if err != nil {
			writeError(c, http.StatusBadRequest, "learned_date 格式应为 YYYY-MM-DD")
			return
		}
		req.LearnedDate = parsed.Format("2006-01-02") // 规范化
	}

	rows, errs := parseVocabCSV(req.Lang, req.CSV)
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, map[string]any{
			"error":  "CSV 校验未通过，未写入任何词条",
			"errors": errs,
		})
		return
	}
	if len(rows) == 0 {
		writeError(c, http.StatusBadRequest, "CSV 中没有可导入的词条")
		return
	}
	if req.DryRun {
		writeJSON(c, http.StatusOK, map[string]any{
			"dry_run": true, "inserted": 0, "updated": 0, "total": len(rows),
		})
		return
	}

	inserted, updated, err := h.store.UpsertVocabWords(c.Request.Context(), rows)
	if err != nil {
		h.logger.Printf("import vocab words lang=%s: %v", req.Lang, err)
		writeError(c, http.StatusInternalServerError, "import vocab words failed")
		return
	}

	resp := map[string]any{
		"dry_run":  false,
		"inserted": inserted,
		"updated":  updated,
		"total":    len(rows),
	}

	if tagLearned {
		lemmas := make([]string, len(rows))
		for i, r := range rows {
			lemmas[i] = r.Lemma
		}
		ids, err := h.store.VocabWordIDsByLemmas(c.Request.Context(), req.Lang, lemmas)
		if err != nil {
			h.logger.Printf("import vocab words: resolve ids failed: %v", err)
			writeError(c, http.StatusInternalServerError, "resolve vocab word ids failed")
			return
		}
		loc := h.now().Location()
		learnedAt, _ := time.ParseInLocation("2006-01-02", req.LearnedDate, loc)
		accepted, ignored, err := h.store.MarkWordsLearnedAt(
			c.Request.Context(), "admin", ids, learnedAt,
		)
		if err != nil {
			h.logger.Printf("import vocab words: mark learned failed: %v", err)
			writeError(c, http.StatusInternalServerError, "mark words learned failed")
			return
		}
		resp["learned_marked"] = accepted
		resp["learned_ignored"] = ignored
	}

	writeJSON(c, http.StatusOK, resp)
}

// parseVocabCSV 解析管理台导入的 CSV。支持与 content/<lang>/vocab.csv 相同的列，
// 额外忽略 builder 专用列；行内 lang 可省略，省略时使用请求语种。
func parseVocabCSV(lang, raw string) ([]store.VocabWordInput, []string) {
	raw = strings.TrimPrefix(raw, "\ufeff")
	reader := csv.NewReader(strings.NewReader(raw))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return nil, []string{"CSV 内容为空"}
		}
		return nil, []string{"CSV 解析失败：" + err.Error()}
	}
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[strings.ToLower(strings.TrimSpace(name))] = i
	}
	var missing []string
	for _, required := range []string{"lemma", "level", "chinese_mean"} {
		if _, ok := col[required]; !ok {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		return nil, []string{"CSV 缺少必需列：" + strings.Join(missing, ", ")}
	}

	pick := func(record []string, name string) string {
		idx, ok := col[name]
		if !ok || idx >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[idx])
	}

	rows := make([]store.VocabWordInput, 0, 64)
	errors := make([]string, 0)
	seen := make(map[string]bool)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errors = append(errors, "CSV 解析失败："+err.Error())
			break
		}
		if len(strings.TrimSpace(strings.Join(record, ""))) == 0 {
			continue
		}
		line, _ := reader.FieldPos(0)
		lemma := pick(record, "lemma")
		if seen[lemma] {
			errors = append(errors, "第 "+strconv.Itoa(line)+" 行：重复词条 "+lemma)
			continue
		}
		seen[lemma] = true
		input, msg := normalizeVocabWord(vocabWordUpsert{
			Lang:         pick(record, "lang"),
			Lemma:        lemma,
			Level:        pick(record, "level"),
			WordType:     pick(record, "word_type"),
			Gender:       pick(record, "gender"),
			Plural:       pick(record, "plural"),
			Phonetic:     pick(record, "phonetic"),
			Kana:         pick(record, "kana"),
			Kanji:        pick(record, "kanji"),
			Romaji:       pick(record, "romaji"),
			Separable:    pick(record, "separable"),
			TransIntr:    pick(record, "trans_intr"),
			CoreCase:     pick(record, "core_case"),
			SortKey:      pick(record, "sort_key"),
			ExtraJSON:    pick(record, "extra_json"),
			ChineseMean:  pick(record, "chinese_mean"),
			EnglishMean:  pick(record, "english_mean"),
			ExampleDE:    pick(record, "example_de"),
			ExampleEN:    pick(record, "example_en"),
			ExampleJA:    pick(record, "example_ja"),
			SourceMDPath: pick(record, "source_md_path"),
		}, lang)
		if msg != "" {
			errors = append(errors, "第 "+strconv.Itoa(line)+" 行："+msg)
			continue
		}
		rows = append(rows, input)
		if len(errors) >= 50 {
			errors = append(errors, "错误过多，仅展示前 50 条")
			break
		}
	}
	return rows, errors
}
