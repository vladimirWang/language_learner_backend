package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestChallengeSubmissionIntegration 需先用全部迁移初始化空库，再设置 PG_TEST_DSN 运行。
func TestChallengeSubmissionIntegration(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN 未设置")
	}
	ctx := context.Background()
	st, err := New(ctx, dsn, 8)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := st.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	openid := "integration-" + suffix
	lang := "t" + suffix
	user, err := st.UpsertWXUser(ctx, openid, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = sqlDB.Exec("DELETE FROM challenge_attempts WHERE openid = $1", openid)
		_, _ = sqlDB.Exec("DELETE FROM daily_challenge_items WHERE challenge_id IN (SELECT id FROM daily_challenges WHERE lang = $1)", lang)
		_, _ = sqlDB.Exec("DELETE FROM daily_challenges WHERE lang = $1", lang)
		_, _ = sqlDB.Exec("DELETE FROM wx_users WHERE id = $1", user.ID)
	})

	if user.Level != "A1" {
		t.Fatalf("新用户默认等级 = %q，期望 A1", user.Level)
	}
	nickname, level := "等级测试用户", "B1"
	user, err = st.UpdateWXUserProfile(ctx, user.ID, &nickname, &level)
	if err != nil {
		t.Fatal(err)
	}
	if user.Nickname != nickname || user.Level != "B1" || user.OpenID != openid {
		t.Fatalf("用户资料更新结果错误：%+v", user)
	}
	level = "A1"
	user, err = st.UpdateWXUserProfile(ctx, user.ID, nil, &level)
	if err != nil {
		t.Fatal(err)
	}
	if user.Nickname != nickname || user.Level != "A1" {
		t.Fatalf("单独更新等级覆盖了其他资料：%+v", user)
	}

	createChallenge := func(day string) int64 {
		t.Helper()
		ch, created, err := st.CreateChallenge(ctx, DailyChallenge{
			Lang: lang, Level: "A1", Day: day, Title: "integration", Status: ChallengeStatusPublished,
		}, []DailyChallengeItem{{
			Seq: 1, Kind: "word", RefID: "word:1",
			Prompt: json.RawMessage(`{"type":"choice","question":"q","options":["a","b"]}`),
			Answer: json.RawMessage(`{"value":"a"}`),
		}})
		if err != nil || !created {
			t.Fatalf("create challenge %s: created=%v err=%v", day, created, err)
		}
		return ch.ID
	}
	submit := func(challengeID int64, stars, score int, day, answer string) ChallengeSubmission {
		t.Helper()
		out, err := st.SubmitChallengeAttempt(ctx, ChallengeAttempt{
			OpenID: openid, ChallengeID: challengeID,
			Answers: json.RawMessage(fmt.Sprintf(`[{"seq":1,"value":%q}]`, answer)),
			Score:   score, Total: 6, Stars: stars,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	day1 := createChallenge("2026-11-24")
	first := submit(day1, 1, 2, "2026-11-24", "first")
	if first.StarsAdded != 1 || first.TotalStars != 1 || first.Attempt.Passed {
		t.Fatalf("一星首次提交不应签到: %+v", first)
	}
	second := submit(day1, 2, 4, "2026-11-24", "second")
	if !second.AlreadySubmitted || second.StarsAdded != 1 || second.TotalStars != 2 || second.Attempt.BestStars != 2 || !second.Attempt.Passed {
		t.Fatalf("补到两星结果错误: %+v", second)
	}
	lower := submit(day1, 1, 2, "2026-11-24", "latest")
	if lower.StarsAdded != 0 || lower.TotalStars != 2 || lower.Attempt.Stars != 1 || lower.Attempt.BestStars != 2 || !lower.Attempt.Passed {
		t.Fatalf("低星重交不应扣星或取消签到: %+v", lower)
	}
	latest, err := st.GetAttempt(ctx, openid, day1)
	if err != nil {
		t.Fatal(err)
	}
	var latestAnswers []struct {
		Seq   int    `json:"seq"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(latest.Answers, &latestAnswers); err != nil || len(latestAnswers) != 1 || latestAnswers[0].Value != "latest" {
		t.Fatalf("应保存最新答案: %s, err=%v", latest.Answers, err)
	}
	third := submit(day1, 3, 6, "2026-11-24", "third")
	if third.StarsAdded != 1 || third.TotalStars != 3 || third.Attempt.BestStars != 3 {
		t.Fatalf("同日重复提交应只补最高星正差额: %+v", third)
	}

	day2 := createChallenge("2026-11-25")
	next := submit(day2, 2, 4, "2026-11-25", "next")
	earlier := createChallenge("2026-11-20")
	backfill := submit(earlier, 2, 4, "2026-11-20", "earlier")
	if todayPassed := next.Attempt.Passed && backfill.Attempt.Passed; !todayPassed {
		t.Fatalf("补交较早关卡应保持签到: next=%+v earlier=%+v", next, backfill)
	}

	before := backfill.TotalStars
	concurrentID := createChallenge("2026-11-26")
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	added := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := st.SubmitChallengeAttempt(ctx, ChallengeAttempt{
				OpenID: openid, ChallengeID: concurrentID,
				Answers: json.RawMessage(fmt.Sprintf(`[{"seq":1,"value":"concurrent-%d"}]`, i)),
				Score:   6, Total: 6, Stars: 3,
			})
			if err != nil {
				errs <- err
				return
			}
			added <- out.StarsAdded
		}(i)
	}
	wg.Wait()
	close(errs)
	close(added)
	for err := range errs {
		t.Errorf("并发提交失败: %v", err)
	}
	totalAdded := 0
	for value := range added {
		totalAdded += value
	}
	updated, err := st.GetWXUserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if totalAdded != 3 || updated.TotalStars != before+3 {
		t.Fatalf("并发补差错误: added=%d total=%d want=%d", totalAdded, updated.TotalStars, before+3)
	}
}
