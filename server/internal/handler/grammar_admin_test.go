package handler

import (
	"strings"
	"testing"

	"github.com/wangfernando/language-learner/server/internal/store"
)

func TestNormalizeGrammarStatus(t *testing.T) {
	cases := map[string]string{
		"approved": store.GrammarStatusApproved,
		"通过":       store.GrammarStatusApproved,
		"Passed":   store.GrammarStatusApproved,
		"驳回":       store.GrammarStatusRejected,
		"no":       store.GrammarStatusRejected,
		"待审":       store.GrammarStatusDraft,
		"unknown":  "",
		"":         "",
	}
	for raw, want := range cases {
		if got := normalizeGrammarStatus(raw); got != want {
			t.Fatalf("normalizeGrammarStatus(%q) = %q，期望 %q", raw, got, want)
		}
	}
}

func TestBuildGrammarDraft(t *testing.T) {
	base := func() (string, string, string, string, string, []string, string, string, string, string, string) {
		return "de", "A1", "artikel", "定冠词", "Der ___ Hund bellt.",
			[]string{"Der", "Die", "Das", "Den"}, "Der", "定冠词 der", "grammar/artikel.md", "sha", "agent-v1"
	}

	t.Run("合法草稿", func(t *testing.T) {
		draft, err := buildGrammarDraft(base())
		if err != nil {
			t.Fatal(err)
		}
		if draft.Status != store.GrammarStatusDraft || draft.Answer != "Der" || draft.GeneratedBy != "agent-v1" {
			t.Fatalf("草稿字段错误：%+v", draft)
		}
	})

	t.Run("答案不在选项中", func(t *testing.T) {
		lang, level, key, label, stem, options, _, explanation, path, sha, by := base()
		if _, err := buildGrammarDraft(lang, level, key, label, stem, options, "Den Hund", explanation, path, sha, by); err == nil {
			t.Fatal("应拒绝不在选项中的答案")
		}
	})

	t.Run("重复选项去重后仍合法", func(t *testing.T) {
		lang, level, key, label, stem, _, answer, explanation, path, sha, by := base()
		draft, err := buildGrammarDraft(lang, level, key, label, stem, []string{"Der", "der", "Die"}, answer, explanation, path, sha, by)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(draft.Options), "der") {
			t.Fatalf("重复选项未去重：%s", draft.Options)
		}
	})

	t.Run("选项不足", func(t *testing.T) {
		lang, level, key, label, stem, _, answer, explanation, path, sha, by := base()
		if _, err := buildGrammarDraft(lang, level, key, label, stem, []string{"Der"}, answer, explanation, path, sha, by); err == nil {
			t.Fatal("应拒绝少于 2 个选项")
		}
	})

	t.Run("非法语种", func(t *testing.T) {
		_, level, key, label, stem, options, answer, explanation, path, sha, by := base()
		if _, err := buildGrammarDraft("DEUTSCH", level, key, label, stem, options, answer, explanation, path, sha, by); err == nil {
			t.Fatal("应拒绝非法语种")
		}
	})
}
