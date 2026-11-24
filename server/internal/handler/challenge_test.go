package handler

import (
	"encoding/json"
	"testing"

	"github.com/wangfernando/language-learner/server/internal/store"
)

func TestGradeChallenge(t *testing.T) {
	items := []store.DailyChallengeItem{
		{Seq: 1, Kind: "word", Answer: json.RawMessage(`{"value":"树"}`), Explanation: "Baum = 树"},
		{Seq: 2, Kind: "word", Answer: json.RawMessage(`{"value":"窗户"}`)},
		{Seq: 3, Kind: "collocation", Answer: json.RawMessage(`{"value":"warten auf +A"}`)},
	}
	answers := []submitAnswer{
		{Seq: 1, Value: "  树 "},
		{Seq: 2, Value: "门"},
		// seq 3 未作答
	}
	score, results := gradeChallenge(items, answers)
	if score != 1 {
		t.Fatalf("score = %d, want 1", score)
	}
	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	if !results[0].Correct || results[1].Correct || results[2].Correct {
		t.Fatalf("unexpected correctness: %+v", results)
	}
	if results[0].CorrectAnswer != "树" || results[0].Explanation != "Baum = 树" {
		t.Fatalf("unexpected result payload: %+v", results[0])
	}
}

func TestGradeChallengeNormalizesCaseAndSpaces(t *testing.T) {
	items := []store.DailyChallengeItem{
		{Seq: 1, Kind: "grammar", Answer: json.RawMessage(`{"value":"Das ist ein Tisch."}`)},
	}
	score, _ := gradeChallenge(items, []submitAnswer{{Seq: 1, Value: "  das ist EIN tisch.  "}})
	if score != 1 {
		t.Fatalf("score = %d, want 1", score)
	}
}

func TestChallengeStars(t *testing.T) {
	cases := []struct {
		name         string
		score, total int
		want         int
	}{
		{"无题目", 0, 0, 0},
		{"零答对始终零星", 0, 5, 0},
		{"六题零星", 0, 6, 0},
		{"六题答对一题零星", 1, 6, 0},
		{"六题答对两题一星", 2, 6, 1},
		{"六题答对三题一星", 3, 6, 1},
		{"六题答对四题两星", 4, 6, 2},
		{"六题答对五题两星", 5, 6, 2},
		{"六题全对三星", 6, 6, 3},
		{"五题答对一题不足百分之六十", 1, 5, 0},
		{"五题答对两题按正确率一星", 2, 5, 1},
		{"五题达到向下取整兜底门槛", 3, 5, 1},
		{"五题答对四题两星", 4, 5, 2},
		{"五题全对三星", 5, 5, 3},
		{"四题兜底门槛", 2, 4, 1},
		{"三题答对一题一星", 1, 3, 1},
		{"三题全对三星", 3, 3, 3},
		{"封顶三星", 2, 2, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := challengeStars(tc.score, tc.total); got != tc.want {
				t.Fatalf("challengeStars(%d, %d) = %d, want %d", tc.score, tc.total, got, tc.want)
			}
		})
	}
}

func TestWXAvatarFileType(t *testing.T) {
	cases := []struct {
		name        string
		head        []byte
		contentType string
		ext         string
		ok          bool
	}{
		{"JPEG", []byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg", "jpg", true},
		{"PNG", []byte("\x89PNG\r\n\x1a\nrest"), "image/png", "png", true},
		{"WebP", []byte("RIFF1234WEBPrest"), "image/webp", "webp", true},
		{"GIF 不支持", []byte("GIF89a"), "", "", false},
		{"空文件", nil, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contentType, ext, ok := wxAvatarFileType(tc.head)
			if contentType != tc.contentType || ext != tc.ext || ok != tc.ok {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", contentType, ext, ok, tc.contentType, tc.ext, tc.ok)
			}
		})
	}
}
