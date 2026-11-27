package handler

import (
	"encoding/json"
	"testing"
)

func TestParseGeneratedVocab(t *testing.T) {
	raw := "```json\n{\"items\":[" +
		`{"lemma":"Hund","word_type":"noun","gender":"m","plural":"Hunde","chinese_mean":"狗","english_mean":"dog","example":"Der Hund bellt.","separable":false},` +
		`{"lemma":"aufstehen","word_type":"verb","chinese_mean":"起床","separable":true}` +
		"]}\n```"
	items, err := parseGeneratedVocab(raw)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(items) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(items))
	}
	if got := separableText(items[0].Separable); got != "false" {
		t.Fatalf("布尔 false 应转为 false，实际 %q", got)
	}
	if got := separableText(items[1].Separable); got != "true" {
		t.Fatalf("布尔 true 应转为 true，实际 %q", got)
	}
	if separableText(json.RawMessage(`"yes"`)) != "yes" {
		t.Fatal("字符串形式应原样返回")
	}

	if _, err := parseGeneratedVocab("not json"); err == nil {
		t.Fatal("非 JSON 输出应报错")
	}
}

func TestNormalizeGeneratedVocab(t *testing.T) {
	req := vocabGenerateRequest{Lang: "de", Level: "A2"}

	if _, _, reason := normalizeGeneratedVocab(generatedVocabItem{
		Lemma: "Hund", WordType: "noun", ChineseMean: "狗",
	}, req); reason == "" {
		t.Fatal("德语名词缺少词性性别应被拒绝")
	}

	candidate, input, reason := normalizeGeneratedVocab(generatedVocabItem{
		Lemma: "Hund", WordType: "noun", Gender: "m", Plural: "Hunde",
		ChineseMean: "狗", EnglishMean: "dog", Example: "Der Hund bellt.",
	}, req)
	if reason != "" {
		t.Fatalf("合法德语名词不应报错：%s", reason)
	}
	if candidate.Level != "A2" || input.Level != "A2" {
		t.Fatal("等级应以请求等级为准")
	}
	if input.ExampleDE != "Der Hund bellt." {
		t.Fatalf("例句应落到 example_de，实际 %q", input.ExampleDE)
	}

	jaReq := vocabGenerateRequest{Lang: "ja", Level: "N5"}
	if _, _, reason := normalizeGeneratedVocab(generatedVocabItem{
		Lemma: "山", WordType: "noun", ChineseMean: "山", Kana: "やま",
	}, jaReq); reason != "" {
		t.Fatalf("合法日语词条不应报错：%s", reason)
	}
}

func TestLevelMatchesLang(t *testing.T) {
	if !levelMatchesLang("de", "A1") || !levelMatchesLang("en", "C1") {
		t.Fatal("de/en 应允许 CEFR")
	}
	if levelMatchesLang("de", "N5") {
		t.Fatal("德语不应允许 JLPT")
	}
	if !levelMatchesLang("ja", "N3") || levelMatchesLang("ja", "A2") {
		t.Fatal("日语只应允许 JLPT")
	}
}
