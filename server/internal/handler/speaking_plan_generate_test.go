package handler

import (
	"encoding/json"
	"testing"

	"github.com/wangfernando/language-learner/server/internal/store"
)

func generatedPlanTestCandidates() []store.SpeakingItem {
	return []store.SpeakingItem{
		{
			ItemKey:  "de-b1-existing-1",
			Lang:     "de",
			Level:    "B1",
			ExamPart: "part1_intro",
			Topic:    "Person",
			PromptDE: "Wie heißen Sie?",
			Active:   true,
		},
		{
			ItemKey:  "de-b1-existing-2",
			Lang:     "de",
			Level:    "B1",
			ExamPart: "part1_intro",
			Topic:    "Wohnen",
			PromptDE: "Wo wohnen Sie?",
			Active:   true,
		},
	}
}

func generatedPlanTestNewItem() *generatedSpeakingNewItem {
	return &generatedSpeakingNewItem{
		ExamPart:       "part3_discussion",
		Topic:          "Umwelt",
		Role:           "examiner",
		PromptDE:       "Was können wir für die Umwelt tun?",
		PromptZH:       "我们能为环境做些什么？",
		ExpectedPoints: []string{"weniger Auto fahren", "Müll trennen"},
		RefAnswer:      "Wir können weniger Auto fahren und Müll trennen.",
		ErrorNote:      "注意情态动词后的动词位置。",
	}
}

func generatedPlanTestJSON(t *testing.T, entries []generatedSpeakingPlanReplyEntry, warnings []string) string {
	t.Helper()
	raw, err := json.Marshal(generatedSpeakingPlanReply{Entries: entries, Warnings: warnings})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseGeneratedSpeakingPlanReplyPreservesOrder(t *testing.T) {
	plan := store.SpeakingPlan{Lang: "de", Level: "B1"}
	raw := generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{
		{NewItem: generatedPlanTestNewItem()},
		{ExistingItemKey: "de-b1-existing-2"},
		{ExistingItemKey: "de-b1-existing-1"},
	}, []string{"  请审核新题  ", ""})

	entries, warnings, err := parseGeneratedSpeakingPlanReply(raw, plan, 3, generatedPlanTestCandidates())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	wantSources := []string{"new", "existing", "existing"}
	wantKeys := []string{"", "de-b1-existing-2", "de-b1-existing-1"}
	for i, entry := range entries {
		if entry.Position != i+1 || entry.Source != wantSources[i] || entry.Item.ItemKey != wantKeys[i] {
			t.Errorf("entry %d = %+v, want position=%d source=%s key=%q", i, entry, i+1, wantSources[i], wantKeys[i])
		}
	}
	if entries[0].Item.Active || entries[0].Item.Lang != "de" || entries[0].Item.Level != "B1" {
		t.Errorf("new item server fields not enforced: %+v", entries[0].Item)
	}
	if entries[0].Item.ExpectedPoints != `["weniger Auto fahren","Müll trennen"]` {
		t.Errorf("unexpected expected_points: %s", entries[0].Item.ExpectedPoints)
	}
	if len(warnings) != 1 || warnings[0] != "请审核新题" {
		t.Errorf("warnings = %#v", warnings)
	}
}

func TestParseGeneratedSpeakingPlanReplyRejectsInvalidOutput(t *testing.T) {
	plan := store.SpeakingPlan{Lang: "de", Level: "B1"}
	candidates := generatedPlanTestCandidates()
	existing := generatedSpeakingPlanReplyEntry{ExistingItemKey: "de-b1-existing-1"}

	tests := []struct {
		name      string
		raw       string
		itemCount int
	}{
		{name: "bad json", raw: "{", itemCount: 1},
		{name: "wrong item count", raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{existing}, nil), itemCount: 2},
		{name: "unknown existing key", raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{{ExistingItemKey: "unknown"}}, nil), itemCount: 1},
		{name: "both sources", raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{{ExistingItemKey: "de-b1-existing-1", NewItem: generatedPlanTestNewItem()}}, nil), itemCount: 1},
		{name: "missing source", raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{{}}, nil), itemCount: 1},
		{name: "duplicate existing key", raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{existing, existing}, nil), itemCount: 2},
		{
			name: "duplicate normalized prompt",
			raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{
				existing,
				{NewItem: &generatedSpeakingNewItem{
					ExamPart: "part1_intro", Topic: "Person", PromptDE: "  wie   heißen Sie? ",
					ExpectedPoints: []string{"Name"},
				}},
			}, nil),
			itemCount: 2,
		},
		{
			name: "empty points",
			raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{{NewItem: &generatedSpeakingNewItem{
				ExamPart: "part1_intro", Topic: "Person", PromptDE: "Was ist Ihr Name?", ExpectedPoints: []string{},
			}}}, nil),
			itemCount: 1,
		},
		{
			name: "blank point",
			raw: generatedPlanTestJSON(t, []generatedSpeakingPlanReplyEntry{{NewItem: &generatedSpeakingNewItem{
				ExamPart: "part1_intro", Topic: "Person", PromptDE: "Was ist Ihr Name?", ExpectedPoints: []string{"  "},
			}}}, nil),
			itemCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseGeneratedSpeakingPlanReply(tt.raw, plan, tt.itemCount, candidates); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
