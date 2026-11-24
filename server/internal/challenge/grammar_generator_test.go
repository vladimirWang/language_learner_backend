package challenge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wangfernando/language-learner/server/internal/store"
)

func TestFilterQuestionsQualityGate(t *testing.T) {
	gen := &GrammarGenerator{promptVer: "v1", modelTag: "test"}
	src := store.GrammarSource{
		Key: "artikel", Label: "定冠词", Level: "A1", SourcePath: "grammar/artikel.md",
		Contents: []string{"定冠词 der/die/das"},
		Hash:     fmt.Sprintf("%x", sha256.Sum256([]byte("定冠词 der/die/das"))),
	}
	seen := map[string]struct{}{}
	lang, level := "de", "A1"

	// 基础合法题
	valid := []llmGrammarQuestion{{
		Stem: "Der ___ Hund bellt.", Options: []string{"Der", "Die", "Das", "Den"},
		Answer: "Der", Explanation: "阳性单数主格。",
	}}
	out, skipped := gen.filterQuestions(valid, lang, level, src, seen)
	if len(out) != 1 || skipped != 0 {
		t.Fatalf("合法题：out=%d skipped=%d", len(out), skipped)
	}

	// 答案不在选项 → 丢弃
	badAnswer := []llmGrammarQuestion{{
		Stem: "Der ___ Hund bellt.", Options: []string{"Der", "Die", "Das", "Den"},
		Answer: "Der Hund", Explanation: "test test test",
	}}
	if _, _ = gen.filterQuestions(badAnswer, lang, level, src, seen); false {
	}
	// 单测中 seen 已被填入上面那道的 stem，换个全新测试函数的 seen
	seen = map[string]struct{}{}
	if out, skipped = gen.filterQuestions(badAnswer, lang, level, src, seen); len(out) != 0 || skipped != 1 {
		t.Fatalf("答案不在选项：out=%d skipped=%d", len(out), skipped)
	}

	// 选项不足 2 → 丢弃
	seen = map[string]struct{}{}
	tooFew := []llmGrammarQuestion{{
		Stem: "Der ___ Hund bellt.", Options: []string{"Der"},
		Answer: "Der", Explanation: "test test test",
	}}
	if out, skipped = gen.filterQuestions(tooFew, lang, level, src, seen); len(out) != 0 || skipped != 1 {
		t.Fatalf("选项不足：out=%d skipped=%d", len(out), skipped)
	}

	// 解释过短 → 丢弃
	seen = map[string]struct{}{}
	shortExp := []llmGrammarQuestion{{
		Stem: "Der ___ Hund bellt.", Options: []string{"Der", "Die", "Das", "Den"},
		Answer: "Der", Explanation: "ok",
	}}
	if out, skipped = gen.filterQuestions(shortExp, lang, level, src, seen); len(out) != 0 || skipped != 1 {
		t.Fatalf("解释过短：out=%d skipped=%d", len(out), skipped)
	}

	// 单 grammar 点内答案分布集中度过高（≥80%）→ 整批丢弃
	seen = map[string]struct{}{}
	heavy := []llmGrammarQuestion{
		{Stem: "Q1 Der ___ Hund.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 1"},
		{Stem: "Q2 Die ___ Frau.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Die", Explanation: "test test test 2"},
		{Stem: "Q3 Das ___ Buch.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 3"},
		{Stem: "Q4 Den ___ Mann.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 4"},
	}
	// 4 题里 Der 占 3 个 = 75%，不触发（阈值 ≥80%）
	if out, skipped = gen.filterQuestions(heavy, lang, level, src, seen); len(out) != 4 || skipped != 0 {
		t.Fatalf("75%% 集中：out=%d skipped=%d", len(out), skipped)
	}
	// 换成 5 道 Der + 1 Die（83%）
	seen = map[string]struct{}{}
	heavy2 := []llmGrammarQuestion{
		{Stem: "Q1 Der ___ Hund.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 1"},
		{Stem: "Q2 Der ___ Hund.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 2"},
		{Stem: "Q3 Der ___ Hund.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 3"},
		{Stem: "Q4 Der ___ Hund.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 4"},
		{Stem: "Q5 Der ___ Hund.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "test test test 5"},
		{Stem: "Q6 Die ___ Frau.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Die", Explanation: "test test test 6"},
	}
	// 注意 stem 重复也会被过滤掉，所以真正进入答案分布的 stem 都是不同的
	seen = map[string]struct{}{}
	if out, skipped = gen.filterQuestions(heavy2, lang, level, src, seen); len(out) != 0 || skipped != 6 {
		t.Fatalf("83%% 集中：out=%d skipped=%d", len(out), skipped)
	}
}

// TestGenerateIntegration 测试 ListGrammarSources + filterQuestions + UpsertGrammarDrafts 的端到端链路，
// 不走真实 LLM（Generator 需 llm.Enabled()，所以直接调内部 filter + store 方法）。
func TestGenerateIntegration(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置")
	}
	ctx := context.Background()
	st, err := store.New(ctx, dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := st.SQLDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	lang := fmt.Sprintf("ggen-%d", time.Now().UnixNano())
	const level = "A1"
	cleanup := func() {
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM grammar_questions WHERE lang = $1", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM rag_chunks WHERE lang = $1 AND chunk_type = 'grammar'", lang)
	}
	t.Cleanup(cleanup)

	// 准备 rag_chunks 语法素材
	cleanup()
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO rag_chunks (lang, level, chunk_type, title, content, source_path, source_hash, patterns, prompts)
		 VALUES ($1, $2, 'grammar', '定冠词', 'Der Hund / Die Frau / Das Kind 三种性别', 'grammar/artikel.md', 'hash-artikel', 'ART + Noun|Der/Die/Das', 'Der Hund bellt.|Die Frau liest.|Das Kind spielt.')`,
		lang, level,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO rag_chunks (lang, level, chunk_type, title, content, source_path, source_hash)
		 VALUES ($1, $2, 'grammar', '动词现在时', 'ich bin / du bist / er ist / wir sind', 'grammar/sein.md', 'hash-sein')`,
		lang, level,
	)
	if err != nil {
		t.Fatal(err)
	}

	sources, err := st.ListGrammarSources(ctx, lang, level, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("语法点数量 = %d，期望 2", len(sources))
	}
	if sources[0].Key == "" || sources[0].Hash == "" || len(sources[0].Contents) == 0 {
		t.Fatalf("GrammarSource 字段缺失：%+v", sources[0])
	}

	// 对第一个 source 模拟 LLM 输出 → 质量门 → 草稿入库
	gen := &GrammarGenerator{promptVer: "v1", modelTag: "test"}
	seen := map[string]struct{}{}
	mockOutput := []llmGrammarQuestion{
		{Stem: "___ Hund bellt laut.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Der", Explanation: "阳性主格，Der Hund。"},
		{Stem: "___ Frau liest ein Buch.", Options: []string{"Der", "Die", "Das", "Den"}, Answer: "Die", Explanation: "阴性主格，Die Frau。"},
	}
	clean, skipped := gen.filterQuestions(mockOutput, lang, level, sources[0], seen)
	if len(clean) != 2 || skipped != 0 {
		t.Fatalf("filterQuestions: clean=%d skipped=%d", len(clean), skipped)
	}
	for _, q := range clean {
		if q.GeneratedBy != "grammar-agent/v1/test" || q.SourcePath != sources[0].SourcePath || q.SourceSHA != sources[0].Hash {
			t.Fatalf("草稿字段错误：%+v", q)
		}
		if q.Status != store.GrammarStatusDraft {
			t.Fatalf("Status 应为 draft，实际 %s", q.Status)
		}
	}

	written, err := st.UpsertGrammarDrafts(ctx, clean)
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Fatalf("写入数 = %d，期望 2", written)
	}

	// 验证题目真正入库为 draft
	list, err := st.ListGrammarQuestions(ctx, lang, level, store.GrammarStatusDraft, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("入库草稿数 = %d，期望 2", len(list))
	}
	for _, q := range list {
		var opts []string
		_ = json.Unmarshal(q.Options, &opts)
		if len(opts) != 4 {
			t.Fatalf("options 解析错误：%s", q.Options)
		}
	}

	// 再次 upsert 同一题干（幂等 + approved 保护）：
	// ON CONFLICT DO UPDATE 即使所有列值未变，Postgres 的 RowsAffected 仍是 1，
	// 所以两条都算更新；但 approved 那条的 status 会保持 approved。
	_, err = st.ReviewGrammarQuestions(ctx, []int64{list[0].ID}, store.GrammarStatusApproved, "ok")
	if err != nil {
		t.Fatal(err)
	}
	repeated := []store.GrammarQuestion{clean[0], clean[1]}
	_, err = st.UpsertGrammarDrafts(ctx, repeated)
	if err != nil {
		t.Fatal(err)
	}
	approvedAfter, err := st.ListGrammarQuestions(ctx, lang, level, store.GrammarStatusApproved, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvedAfter) != 1 {
		t.Fatalf("重新 upsert 后已通过题被覆盖丢失，approved=%d", len(approvedAfter))
	}
	draftAfter, err := st.ListGrammarQuestions(ctx, lang, level, store.GrammarStatusDraft, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(draftAfter) != 1 {
		t.Fatalf("重新 upsert 后草稿数异常，draft=%d", len(draftAfter))
	}
}
