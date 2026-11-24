package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestGrammarReviewIntegration 需先用全部迁移初始化空库，再设置 PG_TEST_DSN 运行。
func TestGrammarReviewIntegration(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := st.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	lang := fmt.Sprintf("gr-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = sqlDB.Exec("DELETE FROM grammar_questions WHERE lang = $1", lang)
	})

	options, _ := json.Marshal([]string{"Der", "Die", "Das"})
	drafts := []GrammarQuestion{
		{Lang: lang, Level: "A1", PointKey: "artikel", PointLabel: "定冠词", Stem: "Der ___ Hund bellt.",
			Options: options, Answer: "Der", Explanation: "der Hund", GeneratedBy: "agent"},
		{Lang: lang, Level: "A1", PointKey: "artikel", PointLabel: "定冠词", Stem: "___ Frau ist nett.",
			Options: options, Answer: "Die", Explanation: "die Frau", GeneratedBy: "agent"},
	}
	written, err := st.UpsertGrammarDrafts(ctx, drafts)
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Fatalf("写入数 = %d，期望 2", written)
	}

	pool, err := st.ChallengeGrammarPool(ctx, lang, "A1", "seed", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 0 {
		t.Fatalf("未审核草稿不应进入关卡：%d", len(pool))
	}

	list, err := st.ListGrammarQuestions(ctx, lang, "A1", GrammarStatusDraft, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("待审题数 = %d，期望 2", len(list))
	}

	if _, err := st.ReviewGrammarQuestions(ctx, []int64{list[0].ID}, GrammarStatusApproved, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReviewGrammarQuestions(ctx, []int64{list[1].ID}, GrammarStatusRejected, "干扰项太弱"); err != nil {
		t.Fatal(err)
	}
	pool, err = st.ChallengeGrammarPool(ctx, lang, "A1", "seed", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 1 || pool[0].Answer != "Der" {
		t.Fatalf("只应返回已通过题：%+v", pool)
	}

	// 重新生成同一题干不得把已通过题打回草稿。
	if _, err := st.UpsertGrammarDrafts(ctx, drafts); err != nil {
		t.Fatal(err)
	}
	approved, err := st.ListGrammarQuestions(ctx, lang, "A1", GrammarStatusApproved, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(approved) != 1 {
		t.Fatalf("已通过题被覆盖，剩余 %d", len(approved))
	}
	rejected, err := st.ListGrammarQuestions(ctx, lang, "A1", GrammarStatusRejected, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 {
		t.Fatalf("已驳回题重新生成后应回到草稿，实际驳回 %d", len(rejected))
	}
}
