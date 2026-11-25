package challenge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wangfernando/language-learner/server/internal/store"
)

func TestNextChallengeTypeCycles(t *testing.T) {
	cases := []struct {
		last string
		want string
	}{
		{"", store.ChallengeTypeVocab},
		{store.ChallengeTypeVocab, store.ChallengeTypeGrammar},
		{store.ChallengeTypeGrammar, store.ChallengeTypeListening},
		{store.ChallengeTypeListening, store.ChallengeTypeVocab},
		{"unknown", store.ChallengeTypeVocab},
	}
	for _, tc := range cases {
		if got := nextChallengeType(tc.last); got != tc.want {
			t.Fatalf("nextChallengeType(%q) = %q, want %q", tc.last, got, tc.want)
		}
	}
}

func TestNewSchedulerAcceptsLegacyParams(t *testing.T) {
	// loc / hour / minute 仅为兼容旧调用方保留，构造不应 panic。
	loc := time.FixedZone("UTC+8", 8*60*60)
	s := NewScheduler(nil, loc, nil, 23, 45)
	if s == nil {
		t.Fatal("scheduler is nil")
	}
}

// TestEnsureCountIntegration 需先用全部迁移初始化空库，再设置 PG_TEST_DSN 运行。
// 验证：按题型轮转生成指定数量的已发布关卡；素材不足的题型自动跳过；重复调用幂等。
func TestEnsureCountIntegration(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置")
	}
	ctx := context.Background()
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	workDir := t.TempDir()
	t.Setenv("WORK_DIR", workDir)
	st, err := store.New(ctx, dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	lang := fmt.Sprintf("sched-%d", time.Now().UnixNano())
	const level = "A1"
	cleanup := func() {
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM daily_challenge_items WHERE challenge_id IN (SELECT id FROM daily_challenges WHERE lang = $1)", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM daily_challenges WHERE lang = $1", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM vocab_words WHERE lang = $1", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM collocations WHERE lang = $1", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM rag_chunks WHERE lang = $1", lang)
	}
	t.Cleanup(cleanup)
	cleanup()

	// 单词 + 搭配 + 语法模板可用，听力无素材（应被跳过）。
	for i := 0; i < 3; i++ {
		if _, err := sqlDB.ExecContext(ctx,
			"INSERT INTO vocab_words (lang, lemma, level, chinese_mean) VALUES ($1, $2, $3, $4)",
			lang, fmt.Sprintf("word-%d", i), level, fmt.Sprintf("释义-%d", i),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(ctx,
			"INSERT INTO collocations (lang, level, phrase, meaning_zh) VALUES ($1, $2, $3, $4)",
			lang, level, fmt.Sprintf("phrase-%d", i), fmt.Sprintf("搭配释义-%d", i),
		); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := sqlDB.ExecContext(ctx,
			`INSERT INTO rag_chunks
			 (lang, level, chunk_type, title, content, source_path, source_hash, prompts)
			 VALUES ($1, $2, 'grammar', $3, $4, $5, $6, $7)`,
			lang, level, fmt.Sprintf("语法-%d", i), "content", fmt.Sprintf("grammar/%d.md", i),
			fmt.Sprintf("hash-%d", i), fmt.Sprintf("语法例句-%d", i),
		); err != nil {
			t.Fatal(err)
		}
	}

	builder := NewBuilder(st)
	const want = 4
	if err := builder.EnsureCount(ctx, lang, level, want); err != nil {
		t.Fatalf("EnsureCount: %v", err)
	}
	got, err := st.ListChallenges(ctx, lang, level)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != want {
		t.Fatalf("生成关卡数 = %d，期望 %d", len(got), want)
	}
	for i, ch := range got {
		if ch.Status != store.ChallengeStatusPublished {
			t.Fatalf("第 %d 关未发布：%+v", i, ch)
		}
		if ch.ID <= 0 {
			t.Fatalf("第 %d 关 id 非法：%d", i, ch.ID)
		}
	}

	// 幂等：再调用一次不应新增。
	if err := builder.EnsureCount(ctx, lang, level, want); err != nil {
		t.Fatalf("EnsureCount 第二次: %v", err)
	}
	got2, err := st.ListChallenges(ctx, lang, level)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != want {
		t.Fatalf("幂等后关卡数 = %d，期望 %d", len(got2), want)
	}
}
