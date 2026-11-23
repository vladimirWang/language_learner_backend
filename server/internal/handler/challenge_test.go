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
