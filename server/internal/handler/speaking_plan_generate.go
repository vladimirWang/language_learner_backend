package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wangfernando/language-learner/server/internal/store"
)

const (
	defaultGeneratedPlanItems = 6
	maxGeneratedPlanItems     = 10
	maxGeneratedPlanDescRunes = 2000
)

type speakingPlanGenerateRequest struct {
	Lang        string `json:"lang"`
	Level       string `json:"level"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ItemCount   *int   `json:"item_count"`
}

type generatedSpeakingNewItem struct {
	ExamPart       string   `json:"exam_part"`
	Topic          string   `json:"topic"`
	Role           string   `json:"role"`
	PromptDE       string   `json:"prompt_de"`
	PromptZH       string   `json:"prompt_zh"`
	ExpectedPoints []string `json:"expected_points"`
	RefAnswer      string   `json:"ref_answer"`
	ErrorNote      string   `json:"error_note"`
}

type generatedSpeakingPlanReplyEntry struct {
	ExistingItemKey string                    `json:"existing_item_key"`
	NewItem         *generatedSpeakingNewItem `json:"new_item"`
}

type generatedSpeakingPlanReply struct {
	Entries  []generatedSpeakingPlanReplyEntry `json:"entries"`
	Warnings []string                          `json:"warnings"`
}

type generatedSpeakingPlanEntry struct {
	Position int                `json:"position"`
	Source   string             `json:"source"`
	Item     store.SpeakingItem `json:"item"`
}

type generatedSpeakingCandidate struct {
	ItemKey        string   `json:"item_key"`
	Topic          string   `json:"topic"`
	ExamPart       string   `json:"exam_part"`
	PromptDE       string   `json:"prompt_de"`
	ExpectedPoints []string `json:"expected_points"`
}

// generateSpeakingPlan 通过一次 LLM 调用选题与补题，并在一个事务内生成停用方案。
func (h *Handler) generateSpeakingPlan(c *gin.Context) {
	var req speakingPlanGenerateRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		writeError(c, http.StatusBadRequest, "description is required")
		return
	}
	if len([]rune(description)) > maxGeneratedPlanDescRunes {
		writeError(c, http.StatusBadRequest, "description is too long (max 2000)")
		return
	}
	itemCount := defaultGeneratedPlanItems
	if req.ItemCount != nil {
		itemCount = *req.ItemCount
	}
	if itemCount < 1 || itemCount > maxGeneratedPlanItems {
		writeError(c, http.StatusBadRequest, "item_count 需在 1–10 之间")
		return
	}
	inactive := false
	plan, msg := speakingPlanMetadataFromRequest(speakingPlanUpsert{
		Lang:        req.Lang,
		Level:       req.Level,
		Name:        req.Name,
		Description: description,
		Active:      &inactive,
	})
	if msg != "" {
		writeError(c, http.StatusBadRequest, msg)
		return
	}
	if !h.llm.Enabled() {
		writeError(c, http.StatusServiceUnavailable, "LLM 未配置，无法智能组卷")
		return
	}

	existingPlans, err := h.store.ListSpeakingPlans(c.Request.Context(), plan.Lang, plan.Level, false)
	if err != nil {
		h.logger.Printf("list speaking plans before generation: %v", err)
		writeError(c, http.StatusInternalServerError, "generate speaking plan failed")
		return
	}
	for _, existing := range existingPlans {
		if existing.Name == plan.Name {
			writeError(c, http.StatusConflict, "该难度下已存在同名方案")
			return
		}
	}

	candidates, err := h.store.ListSpeakingItems(c.Request.Context(), plan.Lang, plan.Level, true)
	if err != nil {
		h.logger.Printf("list speaking items for generation: %v", err)
		writeError(c, http.StatusInternalServerError, "generate speaking plan failed")
		return
	}
	raw, err := h.llm.ChatJSON(
		c.Request.Context(),
		generatedPlanSystemPrompt(plan.Lang, plan.Level, itemCount),
		generatedPlanUserPrompt(plan, itemCount, candidates),
		2000,
	)
	if err != nil {
		h.logger.Printf("generate speaking plan llm: %v", err)
		writeError(c, http.StatusBadGateway, "智能组卷调用失败")
		return
	}
	entries, warnings, err := parseGeneratedSpeakingPlanReply(raw, plan, itemCount, candidates)
	if err != nil {
		h.logger.Printf("generate speaking plan output rejected: %v", err)
		writeError(c, http.StatusBadGateway, "智能组卷结果无效："+err.Error())
		return
	}

	itemKeys := make([]string, 0, len(entries))
	newItems := make([]store.SpeakingItem, 0)
	stamp := time.Now().UnixNano()
	for i := range entries {
		entries[i].Position = i + 1
		if entries[i].Source == "new" {
			entries[i].Item.ItemKey = fmt.Sprintf("%s-%s-gen-%d-%d", plan.Lang, strings.ToLower(plan.Level), stamp, i)
			newItems = append(newItems, entries[i].Item)
		}
		itemKeys = append(itemKeys, entries[i].Item.ItemKey)
	}
	planID, err := h.store.CreateGeneratedSpeakingPlan(c.Request.Context(), plan, itemKeys, newItems)
	if err != nil {
		h.logger.Printf("create generated speaking plan: %v", err)
		writeError(c, http.StatusInternalServerError, "create generated speaking plan failed")
		return
	}
	h.invalidateTopicPlanCache()

	createdPlan, createdItems, err := h.store.GetSpeakingPlan(c.Request.Context(), planID)
	if err != nil {
		h.logger.Printf("get generated speaking plan: %v", err)
		writeError(c, http.StatusInternalServerError, "get generated speaking plan failed")
		return
	}
	itemsByKey := make(map[string]store.SpeakingItem, len(createdItems))
	for _, item := range createdItems {
		itemsByKey[item.ItemKey] = item
	}
	for i := range entries {
		entries[i].Item = itemsByKey[entries[i].Item.ItemKey]
	}
	writeJSON(c, http.StatusOK, map[string]any{
		"plan":     createdPlan,
		"entries":  entries,
		"warnings": warnings,
	})
}

func generatedPlanSystemPrompt(lang, level string, itemCount int) string {
	return fmt.Sprintf(`你是语言口语考试的命题与组卷专家。请根据方案说明，从现有题中优先选择合适的题目，只有说明要求未被现有题覆盖时才创建新题。

硬性要求：
1. 最终必须恰好 %d 题，按实际考试出场顺序排列。
2. 每题只能二选一：existing_item_key 或 new_item，不能同时提供，也不能同时省略。
3. existing_item_key 必须原样来自给定候选题；不得重复选题或重复使用相同问句。
4. 新题必须符合语种 %s、CEFR 等级 %s，prompt_de 使用该语种，prompt_zh 使用中文。
5. 新题的 expected_points 必须是非空字符串数组，每项是简洁、可判定的答案要点；同义表达用 | 分隔。
6. 新题必须完整提供 exam_part、topic、role、prompt_de、prompt_zh、expected_points、ref_answer、error_note。
7. exam_part 只能是 part1_intro、part2_presentation、part3_discussion；role 只能是 examiner 或 partner。
8. 不要指定新题的 item_key、lang、level、active 或 sort_order，这些由服务端生成。
9. 只输出 JSON 对象，格式为 {"entries":[{"existing_item_key":"..."},{"new_item":{...}}],"warnings":["..."]}。没有警告时 warnings 输出 []。`, itemCount, lang, level)
}

func generatedPlanUserPrompt(plan store.SpeakingPlan, itemCount int, candidates []store.SpeakingItem) string {
	existing := make([]generatedSpeakingCandidate, 0, len(candidates))
	for _, item := range candidates {
		existing = append(existing, generatedSpeakingCandidate{
			ItemKey:        item.ItemKey,
			Topic:          item.Topic,
			ExamPart:       item.ExamPart,
			PromptDE:       item.PromptDE,
			ExpectedPoints: parsePointList(item.ExpectedPoints),
		})
	}
	payload, _ := json.Marshal(map[string]any{
		"lang":           plan.Lang,
		"level":          plan.Level,
		"name":           plan.Name,
		"description":    plan.Description,
		"item_count":     itemCount,
		"existing_items": existing,
	})
	return "请按以下输入组卷，优先复用能覆盖方案说明的现有题，并为未覆盖的要求补题：\n" + string(payload)
}

// parseGeneratedSpeakingPlanReply 严格校验模型结果，并覆盖新题的服务端字段。
func parseGeneratedSpeakingPlanReply(raw string, plan store.SpeakingPlan, itemCount int, candidates []store.SpeakingItem) ([]generatedSpeakingPlanEntry, []string, error) {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, nil, fmt.Errorf("返回内容不是 JSON 对象")
	}
	var reply generatedSpeakingPlanReply
	if err := json.Unmarshal([]byte(raw[start:end+1]), &reply); err != nil {
		return nil, nil, fmt.Errorf("JSON 解析失败: %w", err)
	}
	if len(reply.Entries) != itemCount {
		return nil, nil, fmt.Errorf("题目数量为 %d，应为 %d", len(reply.Entries), itemCount)
	}

	candidateByKey := make(map[string]store.SpeakingItem, len(candidates))
	for _, item := range candidates {
		candidateByKey[item.ItemKey] = item
	}
	entries := make([]generatedSpeakingPlanEntry, 0, itemCount)
	seenKeys := make(map[string]bool, itemCount)
	seenPrompts := make(map[string]bool, itemCount)
	for i, rawEntry := range reply.Entries {
		position := i + 1
		key := strings.TrimSpace(rawEntry.ExistingItemKey)
		if (key == "") == (rawEntry.NewItem == nil) {
			return nil, nil, fmt.Errorf("第 %d 题必须且只能选择一个来源", position)
		}

		var entry generatedSpeakingPlanEntry
		if key != "" {
			item, ok := candidateByKey[key]
			if !ok {
				return nil, nil, fmt.Errorf("第 %d 题引用了未知现有题 %q", position, key)
			}
			if seenKeys[key] {
				return nil, nil, fmt.Errorf("第 %d 题重复引用现有题 %q", position, key)
			}
			seenKeys[key] = true
			entry = generatedSpeakingPlanEntry{Position: position, Source: "existing", Item: item}
		} else {
			newItem := rawEntry.NewItem
			if len(newItem.ExpectedPoints) == 0 {
				return nil, nil, fmt.Errorf("第 %d 题缺少答案要点", position)
			}
			points := make([]string, 0, len(newItem.ExpectedPoints))
			for _, point := range newItem.ExpectedPoints {
				point = strings.TrimSpace(point)
				if point == "" {
					return nil, nil, fmt.Errorf("第 %d 题包含空答案要点", position)
				}
				points = append(points, point)
			}
			pointJSON, _ := json.Marshal(points)
			item, msg := speakingItemFromRequest(speakingItemUpsert{
				Lang:           plan.Lang,
				Level:          plan.Level,
				ExamPart:       newItem.ExamPart,
				Topic:          newItem.Topic,
				Role:           newItem.Role,
				PromptDE:       newItem.PromptDE,
				PromptZH:       newItem.PromptZH,
				ExpectedPoints: string(pointJSON),
				RefAnswer:      newItem.RefAnswer,
				ErrorNote:      newItem.ErrorNote,
				SortOrder:      i,
				Active:         false,
			})
			if msg != "" {
				return nil, nil, fmt.Errorf("第 %d 题无效: %s", position, msg)
			}
			entry = generatedSpeakingPlanEntry{Position: position, Source: "new", Item: item}
		}

		prompt := normalizeGeneratedPlanPrompt(entry.Item.PromptDE)
		if seenPrompts[prompt] {
			return nil, nil, fmt.Errorf("第 %d 题与前面的问句重复", position)
		}
		seenPrompts[prompt] = true
		entries = append(entries, entry)
	}

	warnings := make([]string, 0, len(reply.Warnings))
	for _, warning := range reply.Warnings {
		if warning = strings.TrimSpace(warning); warning != "" {
			warnings = append(warnings, warning)
		}
	}
	return entries, warnings, nil
}

func normalizeGeneratedPlanPrompt(prompt string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(prompt)), " "))
}
