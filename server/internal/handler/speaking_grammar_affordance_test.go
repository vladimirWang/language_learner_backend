package handler

import (
	"strings"
	"testing"

	"github.com/wangfernando/language-learner/server/internal/store"
)

func TestCefrHelpers(t *testing.T) {
	if cefrRank("B1") != 3 || cefrRank("a2") != 2 || cefrRank("N5") != 0 {
		t.Fatalf("cefrRank 结果错误")
	}
	if grammarMinRank("A2-B1") != 2 || grammarMinRank("B1") != 3 || grammarMinRank("") != 0 {
		t.Fatalf("grammarMinRank 结果错误")
	}
}

func TestDowngradeRequirementsForAffordance(t *testing.T) {
	points := map[string]store.GrammarPoint{
		"definite_article": {
			Key: "definite_article", Level: "A2", Patterns: []string{`\bder\b`},
		},
		"konjunktiv2": {
			Key: "konjunktiv2", Level: "B1", Patterns: []string{`\bwürde\b`},
		},
	}
	base := func(key, label, enforce string, patterns []string) store.GrammarRequirement {
		return store.GrammarRequirement{
			Key: key, Label: label, Enforce: enforce, Patterns: patterns,
		}
	}

	t.Run("语法等级高于方案时降级", func(t *testing.T) {
		out, warnings := downgradeRequirementsForAffordance(
			"A1",
			[]store.GrammarRequirement{base("definite_article", "定冠词", "block", []string{`\bder\b`})},
			points, nil, false,
		)
		if out[0].Enforce != "warn" {
			t.Fatalf("期望降级为 warn，实际 %s", out[0].Enforce)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "语法等级") {
			t.Fatalf("警告内容错误: %v", warnings)
		}
	})

	t.Run("题库未标注且等级匹配时保留阻断", func(t *testing.T) {
		out, warnings := downgradeRequirementsForAffordance(
			"B1",
			[]store.GrammarRequirement{base("konjunktiv2", "第二虚拟式", "block", []string{`\bwürde\b`})},
			points, nil, false,
		)
		if out[0].Enforce != "block" || len(warnings) != 0 {
			t.Fatalf("不应降级: %+v %v", out, warnings)
		}
	})

	t.Run("有承载题时保留阻断", func(t *testing.T) {
		out, warnings := downgradeRequirementsForAffordance(
			"B1",
			[]store.GrammarRequirement{base("konjunktiv2", "第二虚拟式", "block", []string{`\bwürde\b`})},
			points, map[string]bool{"konjunktiv2": true}, true,
		)
		if out[0].Enforce != "block" || len(warnings) != 0 {
			t.Fatalf("不应降级: %+v %v", out, warnings)
		}
	})

	t.Run("有标注但无承载题时降级", func(t *testing.T) {
		out, warnings := downgradeRequirementsForAffordance(
			"B1",
			[]store.GrammarRequirement{base("konjunktiv2", "第二虚拟式", "block", []string{`\bwürde\b`})},
			points, map[string]bool{"definite_article": true}, true,
		)
		if out[0].Enforce != "warn" || len(warnings) != 1 ||
			!strings.Contains(warnings[0], "没有能自然引出") {
			t.Fatalf("期望因无承载题降级: %+v %v", out, warnings)
		}
	})

	t.Run("语法点缺失时降级", func(t *testing.T) {
		out, warnings := downgradeRequirementsForAffordance(
			"B1",
			[]store.GrammarRequirement{base("missing", "缺失语法", "block", []string{"a"})},
			points, nil, false,
		)
		if out[0].Enforce != "warn" || len(warnings) != 1 {
			t.Fatalf("期望降级: %+v %v", out, warnings)
		}
	})

	t.Run("提示项不受影响", func(t *testing.T) {
		out, warnings := downgradeRequirementsForAffordance(
			"A1",
			[]store.GrammarRequirement{base("definite_article", "定冠词", "warn", []string{`\bder\b`})},
			points, nil, false,
		)
		if out[0].Enforce != "warn" || len(warnings) != 0 {
			t.Fatalf("提示项不应产生降级警告: %+v %v", out, warnings)
		}
	})
}
