package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangfernando/language-learner/server/internal/store"
)

// cefrRank 把 CEFR 等级转成可比较的序号；未知等级返回 0。
func cefrRank(level string) int {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "A1":
		return 1
	case "A2":
		return 2
	case "B1":
		return 3
	case "B2":
		return 4
	case "C1":
		return 5
	case "C2":
		return 6
	default:
		return 0
	}
}

// grammarMinRank 取语法点等级区间的下限，例如 A2-B1 返回 A2 的序号。
func grammarMinRank(level string) int {
	min := 0
	for _, part := range strings.Split(level, "-") {
		rank := cefrRank(part)
		if rank == 0 {
			continue
		}
		if min == 0 || rank < min {
			min = rank
		}
	}
	return min
}

// downgradeRequirementsForAffordance 把没有使用场景的阻断型语法要求降级为提示。
// supported 表示方案内题目声明的可引出语法点；hasTags 为 false 时视为题库尚未标注，
// 只做等级校验，避免历史数据被一次性全部降级。
func downgradeRequirementsForAffordance(
	planLevel string,
	requirements []store.GrammarRequirement,
	pointsByKey map[string]store.GrammarPoint,
	supported map[string]bool,
	hasTags bool,
) ([]store.GrammarRequirement, []string) {
	out := make([]store.GrammarRequirement, len(requirements))
	copy(out, requirements)
	warnings := make([]string, 0)

	for i := range out {
		requirement := &out[i]
		if requirement.Enforce != "block" {
			continue
		}
		label := requirement.Label
		if label == "" {
			label = requirement.Key
		}

		reason := ""
		point, ok := pointsByKey[requirement.Key]
		if !ok && requirement.SourcePath != "" {
			point, ok = pointsByKey[requirement.SourcePath]
		}
		grammarLevel := ""
		if !ok {
			reason = "未找到对应语法点"
		} else {
			grammarLevel = point.Level
		}

		if reason == "" && len(requirement.Patterns) == 0 {
			reason = "缺少判定规则"
		}
		if reason == "" {
			if minRank := grammarMinRank(grammarLevel); minRank > 0 {
				if planRank := cefrRank(planLevel); planRank > 0 && minRank > planRank {
					reason = fmt.Sprintf("语法等级 %s 高于当前方案 %s", grammarLevel, planLevel)
				}
			}
		}
		if reason == "" && hasTags && !supported[requirement.Key] {
			reason = "当前方案没有能自然引出它的题目"
		}
		if reason == "" {
			continue
		}

		requirement.Enforce = "warn"
		warnings = append(warnings, fmt.Sprintf("语法要求「%s」%s，已自动调整为仅提示", label, reason))
	}
	return out, warnings
}

// downgradeUnsupportedGrammarRequirements 读取方案题目与语法点清单，执行承载校验。
func (h *Handler) downgradeUnsupportedGrammarRequirements(
	ctx context.Context,
	plan *store.SpeakingPlan,
	keys []string,
) ([]string, error) {
	if !plan.Active || len(plan.GrammarRequirements) == 0 {
		return []string{}, nil
	}
	requirements := make([]store.GrammarRequirement, 0)
	if err := json.Unmarshal(plan.GrammarRequirements, &requirements); err != nil {
		return nil, err
	}
	if len(requirements) == 0 {
		return []string{}, nil
	}

	items, err := h.store.ListSpeakingItems(ctx, plan.Lang, plan.Level, false)
	if err != nil {
		return nil, err
	}
	planKeys := make(map[string]bool, len(keys))
	for _, key := range keys {
		planKeys[key] = true
	}

	supported := make(map[string]bool)
	hasTags := false
	for _, item := range items {
		if !planKeys[item.ItemKey] || len(item.TargetGrammar) == 0 {
			continue
		}
		var tags []string
		if err := json.Unmarshal(item.TargetGrammar, &tags); err != nil {
			continue
		}
		if len(tags) > 0 {
			hasTags = true
		}
		for _, tag := range tags {
			supported[strings.TrimSpace(tag)] = true
		}
	}

	points, err := h.store.ListGrammarPoints(ctx, plan.Lang, "")
	if err != nil {
		return nil, err
	}
	pointsByKey := make(map[string]store.GrammarPoint, len(points)*2)
	for _, point := range points {
		pointsByKey[point.Key] = point
		if point.SourcePath != "" {
			pointsByKey[point.SourcePath] = point
		}
	}

	downgraded, warnings := downgradeRequirementsForAffordance(
		plan.Level, requirements, pointsByKey, supported, hasTags,
	)
	raw, err := json.Marshal(downgraded)
	if err != nil {
		return nil, err
	}
	plan.GrammarRequirements = store.JSONRaw(raw)
	return warnings, nil
}
