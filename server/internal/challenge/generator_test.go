package challenge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wangfernando/language-learner/server/internal/store"
)

func TestChallengeTypeForDay(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		day  time.Time
		want string
	}{
		{time.Date(2026, 11, 24, 0, 0, 0, 0, loc), store.ChallengeTypeVocab},
		{time.Date(2026, 11, 25, 0, 0, 0, 0, loc), store.ChallengeTypeGrammar},
		{time.Date(2026, 11, 26, 0, 0, 0, 0, loc), store.ChallengeTypeListening},
		{time.Date(2026, 11, 27, 0, 0, 0, 0, loc), store.ChallengeTypeVocab},
		{time.Date(2026, 11, 23, 0, 0, 0, 0, loc), store.ChallengeTypeListening},
	}
	for _, tc := range cases {
		if got := challengeTypeForDay(tc.day); got != tc.want {
			t.Fatalf("%s 题型 = %s，期望 %s", tc.day.Format("2006-01-02"), got, tc.want)
		}
	}
}

func TestListeningCandidates(t *testing.T) {
	manifest := &listeningManifest{
		ExamID: "goethe-a1-1",
		Questions: []listeningQuestion{
			{Number: 1, Audio: "audio/q01.m4a", Start: 1, End: 3.44, Options: []string{"a", "b", "c"}, Answer: json.RawMessage(`"B"`), Transcript: "text-1"},
			{Number: 2, Audio: "audio/q02.m4a", Options: []string{"x", "y"}, Answer: json.RawMessage(`"R"`)},
			{Number: 3, Audio: "audio/q03.m4a", Options: []string{"a", "b"}, Answer: json.RawMessage(`null`)},
			{Number: 4, Audio: "../escape.m4a", Options: []string{"a", "b"}, Answer: json.RawMessage(`"A"`)},
			{Number: 5, Audio: "audio/q05.m4a", Options: []string{}, Answer: json.RawMessage(`"A"`)},
		},
	}
	got := listeningCandidates("goethe-a1-1", manifest)
	if len(got) != 2 {
		t.Fatalf("候选数 = %d，期望 2：%+v", len(got), got)
	}
	if got[0].Answer != "b" || got[0].Audio != "listening/goethe-a1-1/audio/q01.m4a" {
		t.Fatalf("第 1 题解析错误：%+v", got[0])
	}
	if got[0].Duration != 2.4 {
		t.Fatalf("时长 = %v，期望 2.4", got[0].Duration)
	}
	if got[1].Answer != "Richtig" || len(got[1].Options) != 2 {
		t.Fatalf("判断题解析错误：%+v", got[1])
	}
}

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

	workDir := t.TempDir()
	t.Setenv("WORK_DIR", workDir)
	st, err := store.New(ctx, dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	lang := fmt.Sprintf("generator-%d", time.Now().UnixNano())
	const level = "A1"
	cleanup := func() {
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM daily_challenge_items WHERE challenge_id IN (SELECT id FROM daily_challenges WHERE lang = $1)", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM daily_challenges WHERE lang = $1", lang)
		_, _ = sqlDB.ExecContext(ctx, "DELETE FROM listening_jobs WHERE lang = $1", lang)
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
	builder := NewBuilder(st)

	cases := []struct {
		name         string
		words        int
		collocations int
		grammar      int
		challenge    string
		wantTotal    int
		wantKinds    map[string]int
	}{
		{name: "单词关六题", words: 3, collocations: 3, challenge: store.ChallengeTypeVocab, wantTotal: 6,
			wantKinds: map[string]int{"word": 3, "collocation": 3}},
		{name: "单词关五题降级", words: 3, collocations: 2, challenge: store.ChallengeTypeVocab, wantTotal: 5,
			wantKinds: map[string]int{"word": 3, "collocation": 2}},
		{name: "单词关四题降级", words: 2, collocations: 2, challenge: store.ChallengeTypeVocab, wantTotal: 4,
			wantKinds: map[string]int{"word": 2, "collocation": 2}},
		{name: "语法关六题", grammar: 6, challenge: store.ChallengeTypeGrammar, wantTotal: 6,
			wantKinds: map[string]int{"grammar": 6}},
		{name: "语法关模板降级", grammar: 3, challenge: store.ChallengeTypeGrammar, wantTotal: 3,
			wantKinds: map[string]int{"grammar": 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed(tc.words, tc.collocations, tc.grammar)
			items, err := builder.build(ctx, lang, level, "2026-11-24", tc.challenge)
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

	t.Run("手动指定题型并修改草稿", func(t *testing.T) {
		seed(3, 3, 6)
		day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
		created, items, err := builder.Regenerate(ctx, lang, level, day, store.ChallengeTypeGrammar)
		if err != nil {
			t.Fatal(err)
		}
		if created.ChallengeType != store.ChallengeTypeGrammar || created.Status != store.ChallengeStatusDraft {
			t.Fatalf("手动生成结果错误：%+v", created)
		}
		for _, item := range items {
			if item.Kind != "grammar" {
				t.Fatalf("指定语法关却生成 %s 题", item.Kind)
			}
		}

		updated, items, err := builder.Regenerate(ctx, lang, level, day, store.ChallengeTypeVocab)
		if err != nil {
			t.Fatal(err)
		}
		if updated.ID != created.ID || updated.ChallengeType != store.ChallengeTypeVocab {
			t.Fatalf("草稿题型修改失败：%+v", updated)
		}
		if len(items) != 6 {
			t.Fatalf("修改后题数 = %d，期望 6", len(items))
		}
		rows, err := st.ListChallengesForAdmin(ctx, lang, level)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ChallengeType != store.ChallengeTypeVocab {
			t.Fatalf("管理台关卡列表未返回关卡类型：%+v", rows)
		}
	})

	t.Run("不足三题不生成", func(t *testing.T) {
		seed(2, 0, 0)
		items, err := builder.build(ctx, lang, level, "2026-11-24", store.ChallengeTypeVocab)
		if !errors.Is(err, ErrNoContent) || items != nil {
			t.Fatalf("items=%v err=%v，期望 ErrNoContent", items, err)
		}
	})

	t.Run("听力关六题", func(t *testing.T) {
		seed(0, 0, 0)
		examID := "exam-" + lang
		dir := filepath.Join(workDir, "listening", examID, "audio")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		questions := make([]map[string]any, 0, 8)
		for i := 1; i <= 8; i++ {
			file := fmt.Sprintf("q%02d.m4a", i)
			if err := os.WriteFile(filepath.Join(dir, file), []byte("audio"), 0o644); err != nil {
				t.Fatal(err)
			}
			questions = append(questions, map[string]any{
				"number": i, "start": 0, "end": 5, "audio": "audio/" + file,
				"options": []string{"opt-a", "opt-b", "opt-c"}, "answer": "A",
				"transcript": fmt.Sprintf("text-%d", i),
			})
		}
		manifest, err := json.Marshal(map[string]any{"exam_id": examID, "questions": questions})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workDir, "listening", examID, "manifest.json"), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := sqlDB.ExecContext(ctx,
			`INSERT INTO listening_jobs (job_id, exam_id, lang, level, source_url, status)
			 VALUES ($1, $2, $3, $4, 'https://example.com', 'built')`,
			"job-"+lang, examID, lang, level,
		); err != nil {
			t.Fatal(err)
		}
		items, err := builder.build(ctx, lang, level, "2026-11-24", store.ChallengeTypeListening)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 6 {
			t.Fatalf("听力题数 = %d，期望 6", len(items))
		}
		var prompt audioChoicePrompt
		if err := json.Unmarshal(items[0].Prompt, &prompt); err != nil {
			t.Fatal(err)
		}
		if prompt.Type != "audio_choice" || prompt.Audio == "" || len(prompt.Options) != 3 {
			t.Fatalf("听力题面错误：%+v", prompt)
		}
	})
}
