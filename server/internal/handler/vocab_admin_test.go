package handler

import "testing"

func TestParseVocabCSV(t *testing.T) {
	t.Run("解析标准表头并忽略额外列", func(t *testing.T) {
		raw := "\ufefflang,lemma,word_type,gender,plural,separable,trans_intr,level,chinese_mean,english_mean,source_md_path\n" +
			"de,Hund,noun,m,Hunde,,,A2,狗,dog,de/vocab/Hund.md\n" +
			"de,helfen,verb,,,,,A2,帮助,help,de/vocab/helfen.md\n"
		rows, errs := parseVocabCSV("de", raw)
		if len(errs) != 0 {
			t.Fatalf("不应有错误：%v", errs)
		}
		if len(rows) != 2 {
			t.Fatalf("期望 2 行，实际 %d", len(rows))
		}
		if rows[0].Lemma != "Hund" || rows[0].Gender != "m" || rows[0].Plural != "Hunde" {
			t.Fatalf("首行解析错误：%+v", rows[0])
		}
		if rows[1].WordType != "verb" || rows[1].Gender != "" {
			t.Fatalf("次行解析错误：%+v", rows[1])
		}
	})

	t.Run("lang 可省略并回落到请求语种", func(t *testing.T) {
		raw := "lemma,word_type,level,chinese_mean\nschön,adj,A1,漂亮的\n"
		rows, errs := parseVocabCSV("de", raw)
		if len(errs) != 0 || len(rows) != 1 || rows[0].Lang != "de" {
			t.Fatalf("回落语种失败：rows=%+v errs=%v", rows, errs)
		}
	})

	t.Run("缺少必需列时报错", func(t *testing.T) {
		_, errs := parseVocabCSV("de", "lemma,level\nHund,A2\n")
		if len(errs) == 0 {
			t.Fatal("缺少 chinese_mean 应报错")
		}
	})

	t.Run("德语名词缺少词性性别时报错", func(t *testing.T) {
		raw := "lemma,word_type,level,chinese_mean\nHund,noun,A2,狗\n"
		_, errs := parseVocabCSV("de", raw)
		if len(errs) == 0 {
			t.Fatal("德语名词缺 gender 应报错")
		}
	})

	t.Run("非名词填写性别时报错", func(t *testing.T) {
		raw := "lemma,word_type,gender,level,chinese_mean\nhelfen,verb,m,A2,帮助\n"
		_, errs := parseVocabCSV("de", raw)
		if len(errs) == 0 {
			t.Fatal("非名词填 gender 应报错")
		}
	})

	t.Run("重复词条时报错", func(t *testing.T) {
		raw := "lemma,word_type,level,chinese_mean\nHund,noun,A2,狗\nHund,noun,A2,狗\n"
		if _, errs := parseVocabCSV("de", raw); len(errs) == 0 {
			t.Fatal("重复 lemma 应报错")
		}
	})

	t.Run("lang 与请求语种不一致时报错", func(t *testing.T) {
		raw := "lang,lemma,word_type,level,chinese_mean\nen,dog,noun,A2,狗\n"
		if _, errs := parseVocabCSV("de", raw); len(errs) == 0 {
			t.Fatal("lang 不一致应报错")
		}
	})
}

func TestNormalizeVocabWordLevels(t *testing.T) {
	base := vocabWordUpsert{Lemma: "Hund", WordType: "noun", Gender: "m", Level: "a2", ChineseMean: "狗"}
	if _, msg := normalizeVocabWord(base, "de"); msg != "" {
		t.Fatalf("合法词条不应报错：%s", msg)
	}
	bad := base
	bad.Level = "Z9"
	if _, msg := normalizeVocabWord(bad, "de"); msg == "" {
		t.Fatal("非法等级应报错")
	}
}

func TestNormalizeVocabWordEnglishNoGenderPlural(t *testing.T) {
	base := vocabWordUpsert{Lemma: "apple", WordType: "noun", Level: "A1", ChineseMean: "苹果"}
	if _, msg := normalizeVocabWord(base, "en"); msg != "" {
		t.Fatalf("英语名词不应要求词性性别：%s", msg)
	}

	withGender := base
	withGender.Gender = "m"
	if _, msg := normalizeVocabWord(withGender, "en"); msg == "" {
		t.Fatal("英语词条填写词性性别应报错")
	}

	withPlural := base
	withPlural.Plural = "apples"
	if _, msg := normalizeVocabWord(withPlural, "en"); msg == "" {
		t.Fatal("英语词条填写复数应报错")
	}
}

func TestNormalizeVocabWordLangSpecificFields(t *testing.T) {
	en := vocabWordUpsert{
		Lemma: "apple", WordType: "noun", Level: "A1", ChineseMean: "苹果",
		Phonetic: "/ˈæpl/", ExampleEN: "I ate an apple.",
	}
	if _, msg := normalizeVocabWord(en, "en"); msg != "" {
		t.Fatalf("英语专有字段应通过：%s", msg)
	}
	enWithKana := en
	enWithKana.Kana = "りんご"
	if _, msg := normalizeVocabWord(enWithKana, "en"); msg == "" {
		t.Fatal("英语词条填写假名应报错")
	}
	enWithDE := en
	enWithDE.ExampleDE = "Ich aß einen Apfel."
	if _, msg := normalizeVocabWord(enWithDE, "en"); msg == "" {
		t.Fatal("英语词条填写德语例句应报错")
	}

	ja := vocabWordUpsert{
		Lemma: "山", WordType: "noun", Level: "N5", ChineseMean: "山",
		Kana: "やま", Kanji: "山", Romaji: "yama", ExampleJA: "山に登る。",
	}
	if _, msg := normalizeVocabWord(ja, "ja"); msg != "" {
		t.Fatalf("日语专有字段应通过：%s", msg)
	}
	jaWithPhonetic := ja
	jaWithPhonetic.Phonetic = "/jama/"
	if _, msg := normalizeVocabWord(jaWithPhonetic, "ja"); msg == "" {
		t.Fatal("日语词条填写音标应报错")
	}

	if _, msg := normalizeVocabWord(
		vocabWordUpsert{Lemma: "chat", WordType: "noun", Level: "A1", ChineseMean: "猫"}, "fr",
	); msg == "" {
		t.Fatal("暂不支持的语种应报错")
	}
}
