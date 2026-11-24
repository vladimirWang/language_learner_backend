package challenge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wangfernando/language-learner/server/internal/store"
)

// TestBuildItemCountsIntegration 需先用全部迁移初始化空库，再设置 PG_TEST_DSN 运行。
func TestBuildItemCountsIntegration(t *testing.T) {
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
	st, err := store.New(ctx, dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	lang := fmt.Sprintf("generator-%d", time.Now().UnixNano())
	const level = "A1"
	cleanup := func() {
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM vocab_words WHERE lang = $1", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM collocations WHERE lang = $1", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM rag_chunks WHERE lang = $1", lang)
	}
	t.Cleanup(cleanup)

	seed := func(words, collocations, grammar int) {
		t.Helper()
		cleanup()
		for i := 0; i < words; i++ {
			if _, err := sqlDB.ExecContext(ctx,
				"INSERT INTO vocab_words (lang, lemma, level, chinese_mean) VALUES ($1, $2, $3, $4)",
				lang, fmt.Sprintf("word-%d", i), level, fmt.Sprintf("释义-%d", i),
			); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < collocations; i++ {
			if _, err := sqlDB.ExecContext(ctx,
				"INSERT INTO collocations (lang, level, phrase, meaning_zh) VALUES ($1, $2, $3, $4)",
				lang, level, fmt.Sprintf("phrase-%d", i), fmt.Sprintf("搭配释义-%d", i),
			); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < grammar; i++ {
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
	}

	cases := []struct {
		name         string
		words        int
		collocations int
		grammar      int
		wantTotal    int
		wantKinds    map[string]int
	}{
		{name: "标准六题", words: 3, collocations: 2, grammar: 2, wantTotal: 6, wantKinds: map[string]int{"word": 3, "collocation": 2, "grammar": 1}},
		{name: "五题降级", words: 3, collocations: 2, wantTotal: 5, wantKinds: map[string]int{"word": 3, "collocation": 2}},
		{name: "四题降级", words: 2, collocations: 2, wantTotal: 4, wantKinds: map[string]int{"word": 2, "collocation": 2}},
		{name: "三题降级", words: 1, collocations: 2, wantTotal: 3, wantKinds: map[string]int{"word": 1, "collocation": 2}},
	}
	builder := NewBuilder(st)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed(tc.words, tc.collocations, tc.grammar)
			items, _, err := builder.build(ctx, lang, level, "2026-11-24")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != tc.wantTotal {
				t.Fatalf("题数 = %d，期望 %d", len(items), tc.wantTotal)
			}
			gotKinds := make(map[string]int)
			for i, item := range items {
				if item.Seq != i+1 {
					t.Fatalf("第 %d 题 seq = %d", i, item.Seq)
				}
				gotKinds[item.Kind]++
			}
			for kind, want := range tc.wantKinds {
				if gotKinds[kind] != want {
					t.Fatalf("%s 题数 = %d，期望 %d；全部题型：%v", kind, gotKinds[kind], want, gotKinds)
				}
			}
		})
	}

	t.Run("不足三题不生成", func(t *testing.T) {
		seed(2, 0, 0)
		items, _, err := builder.build(ctx, lang, level, "2026-11-24")
		if !errors.Is(err, ErrNoContent) || items != nil {
			t.Fatalf("items=%v err=%v，期望 ErrNoContent", items, err)
		}
	})
}
