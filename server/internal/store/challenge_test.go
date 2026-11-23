package store

import "testing"

func TestNormalizeStreak(t *testing.T) {
	day := func(value string) *string { return &value }
	cases := []struct {
		name    string
		streak  UserStreak
		today   string
		current int
	}{
		{"从未打卡", UserStreak{}, "2026-11-23", 0},
		{"今天已打卡", UserStreak{Current: 5, Longest: 9, LastDay: day("2026-11-23")}, "2026-11-23", 5},
		{"昨天已打卡", UserStreak{Current: 5, Longest: 9, LastDay: day("2026-11-22")}, "2026-11-23", 5},
		{"断档两天", UserStreak{Current: 5, Longest: 9, LastDay: day("2026-11-21")}, "2026-11-23", 0},
		{"跨月连续", UserStreak{Current: 3, Longest: 3, LastDay: day("2026-10-31")}, "2026-11-01", 3},
		{"未来日期异常", UserStreak{Current: 3, Longest: 3, LastDay: day("2026-11-25")}, "2026-11-23", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeStreak(tc.streak, tc.today)
			if got.Current != tc.current {
				t.Fatalf("current = %d, want %d", got.Current, tc.current)
			}
			if got.Longest != tc.streak.Longest {
				t.Fatalf("longest = %d, want %d", got.Longest, tc.streak.Longest)
			}
		})
	}
}
