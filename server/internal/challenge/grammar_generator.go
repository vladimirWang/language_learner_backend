// Package challenge 中的语法题 Agent：从 rag_chunks 聚合的 GrammarSource 出发，
// 用 LLM 生成单选题草稿，经质量门后批量写入 grammar_questions.status='draft'。
// 设计原则：
//  1. 运行时不调用，只在管理接口 /api/admin/grammar/generate 触发；
//  2. 模型必须返回 JSON，质量门硬校验后才入库；
//  3. 已通过的题不因重新生成而被覆盖（store.UpsertGrammarDrafts 保证）。
package challenge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangfernando/language-learner/server/internal/llm"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// GrammarGenerator 封装从素材到草稿入库的完整链路。
type GrammarGenerator struct {
	store     *store.Store
	llm       *llm.Client
	promptVer string // prompt 版本号，写入 generated_by 便于整批回滚
	modelTag  string // 模型标识，同样写入 generated_by

	// MaxPerSource 每个语法点最多生成几题；0 或 >8 时 NewGrammarGenerator 会重置为 4。
	MaxPerSource int

	// Progress 在每个语法点处理完后调用；可为 nil。
	// 参数：序号 i / 总数 total / 当前 source / LLM 返回题数 / 质量门通过并入库数 / 错误（如果有）。
	Progress func(i, total int, src store.GrammarSource, returned, accepted int, err error)
}

// NewGrammarGenerator 构造器；llm 可传 nil（离线测试或 LLM 未配置），此时 Generate 返回 ErrLLMNotConfigured。
func NewGrammarGenerator(st *store.Store, client *llm.Client) *GrammarGenerator {
	return &GrammarGenerator{
		store:        st,
		llm:          client,
		promptVer:    "v1",
		modelTag:     llmModelName(client),
		MaxPerSource: 4,
	}
}

// ErrLLMNotConfigured 表示运行环境未配置 LLM 凭证。
var ErrLLMNotConfigured = errors.New("llm not configured")

// GenerateResult 是一次批量生成的统计。
type GenerateResult struct {
	Sources      int                 `json:"sources"`       // 实际触发生成的语法点数
	TotalDraft   int                 `json:"total_draft"`   // 质量门通过的草稿总数
	SkippedLLM   int                 `json:"skipped_llm"`   // LLM 报错或未配置而跳过的点数
	SkippedEmpty int                 `json:"skipped_empty"` // LLM 返回 0 题的点数
	SkippedDedup int                 `json:"skipped_dedup"` // 质量门过滤后的剩余被去重丢弃的题数
	PerSource    []PerSourceGenerate `json:"per_source"`
}

type PerSourceGenerate struct {
	SourcePath string `json:"source_path"`
	Label      string `json:"label"`
	Requested  int    `json:"requested"`
	Returned   int    `json:"returned"`
	Accepted   int    `json:"accepted"`
	Error      string `json:"error,omitempty"`
}

// Generate 批量生成语法题草稿。
//
//	lang / level：与 ListGrammarSources 参数一致；
//	perSource：每个语法点最多生成几题，0 时用默认 4，上限 8。
//
// 即使中途某个语法点 LLM 失败，也会继续处理其他点；函数只在完全没有可生成内容时返回 nil Result。
func (g *GrammarGenerator) Generate(ctx context.Context, lang, level string, perSource int) (*GenerateResult, error) {
	if g.llm == nil || !g.llm.Enabled() {
		return nil, ErrLLMNotConfigured
	}
	if perSource <= 0 || perSource > 8 {
		perSource = g.MaxPerSource
	}

	sources, err := g.store.ListGrammarSources(ctx, lang, level, 200)
	if err != nil {
		return nil, fmt.Errorf("list grammar sources: %w", err)
	}
	if len(sources) == 0 {
		return &GenerateResult{}, nil
	}

	res := &GenerateResult{Sources: len(sources), PerSource: make([]PerSourceGenerate, 0, len(sources))}
	globalStems := make(map[string]struct{})

	for i, src := range sources {
		per := PerSourceGenerate{SourcePath: src.SourcePath, Label: src.Label, Requested: perSource}
		var returned, accepted int
		var srcErr error

		raw, err := g.callLLM(ctx, src, perSource)
		if err != nil {
			per.Error = err.Error()
			res.SkippedLLM++
			srcErr = err
		} else {
			returned = len(raw)
			per.Returned = returned
			clean, skipped := g.filterQuestions(raw, lang, level, src, globalStems)
			res.SkippedDedup += skipped
			if len(clean) == 0 {
				res.SkippedEmpty++
			} else {
				written, err := g.store.UpsertGrammarDrafts(ctx, clean)
				if err != nil {
					per.Error = err.Error()
					res.SkippedLLM++
					srcErr = err
				} else {
					accepted = written
					per.Accepted = written
					res.TotalDraft += written
				}
			}
		}

		res.PerSource = append(res.PerSource, per)
		if g.Progress != nil {
			g.Progress(i+1, len(sources), src, returned, accepted, srcErr)
		}
	}
	return res, nil
}

// callLLM 单次 LLM 调用：让模型输出 JSON 数组。
func (g *GrammarGenerator) callLLM(ctx context.Context, src store.GrammarSource, perSource int) ([]llmGrammarQuestion, error) {
	system := grammarSystemPrompt()
	user := grammarUserPrompt(src, perSource)
	raw, err := g.llm.ChatJSON(ctx, system, user, 1800)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Questions []llmGrammarQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil || len(wrapper.Questions) == 0 {
		// 兼容模型直接输出数组而不是对象
		var direct []llmGrammarQuestion
		if err2 := json.Unmarshal([]byte(raw), &direct); err2 == nil && len(direct) > 0 {
			wrapper.Questions = direct
		} else {
			return nil, fmt.Errorf("llm 返回无法解析：%w", err)
		}
	}
	return wrapper.Questions, nil
}

// filterQuestions 做硬校验 + 去重 + 补齐 generated_by。
// 规则：
//   - 语种/等级非法 → 丢弃；
//   - 题干为空或重复 → 丢弃；
//   - 选项 <2 / >6 个 → 丢弃；
//   - 答案不在选项中或选项重复 → 丢弃；
//   - 整批答案分布过于集中（≥80% 是同一选项）→ 丢弃整份（该 grammar 点重做需要人工干预，但先把合格题入库）。
func (g *GrammarGenerator) filterQuestions(raw []llmGrammarQuestion, lang, level string, src store.GrammarSource, globalStems map[string]struct{}) ([]store.GrammarQuestion, int) {
	out := make([]store.GrammarQuestion, 0, len(raw))
	skipped := 0
	stemSet := make(map[string]struct{}, len(raw))

	for _, q := range raw {
		q.Stem = strings.TrimSpace(q.Stem)
		if q.Stem == "" {
			skipped++
			continue
		}
		if _, dup := stemSet[q.Stem]; dup {
			skipped++
			continue
		}
		if _, dup := globalStems[q.Stem]; dup {
			skipped++
			continue
		}
		stemSet[q.Stem] = struct{}{}
		globalStems[q.Stem] = struct{}{}

		// 清理选项：去空格、大小写重复去掉、空的去掉
		options := make([]string, 0, len(q.Options))
		optLower := make(map[string]bool)
		for _, opt := range q.Options {
			opt = strings.TrimSpace(opt)
			if opt == "" {
				continue
			}
			lw := strings.ToLower(opt)
			if optLower[lw] {
				continue
			}
			optLower[lw] = true
			options = append(options, opt)
		}
		if len(options) < 2 || len(options) > 6 {
			skipped++
			continue
		}
		q.Answer = strings.TrimSpace(q.Answer)
		matched := ""
		for _, opt := range options {
			if strings.EqualFold(opt, q.Answer) {
				matched = opt
				break
			}
		}
		if matched == "" {
			skipped++
			continue
		}
		q.Explanation = strings.TrimSpace(q.Explanation)
		if len([]rune(q.Explanation)) < 4 {
			skipped++
			continue
		}

		encoded, err := json.Marshal(options)
		if err != nil {
			skipped++
			continue
		}
		out = append(out, store.GrammarQuestion{
			Lang:        lang,
			Level:       level,
			PointKey:    src.Key,
			PointLabel:  src.Label,
			Stem:        q.Stem,
			Options:     encoded,
			Answer:      matched,
			Explanation: q.Explanation,
			SourcePath:  src.SourcePath,
			SourceSHA:   src.Hash,
			GeneratedBy: g.generatedBy(),
			Status:      store.GrammarStatusDraft,
		})
	}

	// 单 grammar 点内部答案分布集中度过高 → 丢弃整份（这批题质量可疑）。
	if len(out) >= 3 {
		counts := make(map[string]int, len(out))
		for _, q := range out {
			counts[q.Answer]++
		}
		maxCount := 0
		for _, c := range counts {
			if c > maxCount {
				maxCount = c
			}
		}
		if float64(maxCount)/float64(len(out)) >= 0.8 {
			skipped += len(out)
			out = out[:0]
		}
	}

	return out, skipped
}

func (g *GrammarGenerator) generatedBy() string {
	return fmt.Sprintf("grammar-agent/%s/%s", g.promptVer, g.modelTag)
}

type llmGrammarQuestion struct {
	Stem        string   `json:"stem"`
	Options     []string `json:"options"`
	Answer      string   `json:"answer"`
	Explanation string   `json:"explanation"`
}

func grammarSystemPrompt() string {
	return strings.Join([]string{
		"你是一位德语 A1/A2 语法题出题专家。任务：根据提供的语法素材，生成适合德语 A1 或 A2 水平学习者的单选题。",
		"严格要求：",
		"1. 每题只有 1 个正确答案；",
		"2. 干扰项（错误选项）必须在语法上说得通、德语母语者看了也会犹豫，不能是明显错误或毫不相关的词；",
		"3. 题干必须包含一个下划线 ____ ，下划线所在位置就是要选的词或短语；",
		"4. 题干和所有选项都用德语，解析可以用中文；",
		"5. 四个选项不要按字母顺序排布，正确答案的位置每次都不一样；",
		"6. 不要生成 '以上都对' 或 '以上都不对' 这类选项；",
		"7. 只输出 JSON 对象，结构为 {\"questions\": [{\"stem\":... ,\"options\":[...],\"answer\":\"...\",\"explanation\":\"...\"}, ...]}；questions 数量等于 requested。",
		"8. answer 字段的值必须完全等于 options 数组中的某个元素（大小写也完全一致）。",
	}, "\n")
}

func grammarUserPrompt(src store.GrammarSource, requested int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "语法点标签：%s\n", src.Label)
	fmt.Fprintf(&b, "预期等级：%s\n", src.Level)
	fmt.Fprintf(&b, "题目数量：%d\n", requested)
	if len(src.Patterns) > 0 {
		fmt.Fprintf(&b, "结构模式：\n")
		for _, p := range src.Patterns {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if len(src.Prompts) > 0 {
		fmt.Fprintf(&b, "例句：\n")
		for _, p := range src.Prompts {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	fmt.Fprintf(&b, "语法素材正文（按 chunk 顺序拼接）：\n")
	for i, c := range src.Contents {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, c)
	}
	fmt.Fprintf(&b, "\n请基于上面的语法素材，生成 %d 道不同的单选题。每道题都要切中该语法点的一个细节，不要让多道题考同一种场景。", requested)
	return b.String()
}

// llmModelName 从客户端推断模型标识，用于 generated_by 字段。
func llmModelName(c *llm.Client) string {
	if c == nil {
		return "unknown"
	}
	// llm.Client 字段未导出，留空避免引入额外接口；handler 可显式覆盖。
	return "default"
}

// randomHex 生成短随机串供 generated_by 防冲突（非必须，保留给未来批量任务）。
func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
