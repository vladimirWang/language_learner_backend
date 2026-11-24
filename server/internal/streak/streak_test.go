package streak

import (
	"testing"
	"time"
)

// bitmapOf 按日期构造测试用 bitmap。
func bitmapOf(t *testing.T, days ...string) []byte {
	t.Helper()
	raw := make([]byte, 0, 64)
	for _, day := range days {
		bit, err := dayOffset(day)
		if err != nil {
			t.Fatal(err)
		}
		index := int(bit / 8)
		for len(raw) <= index {
			raw = append(raw, 0)
		}
		raw[index] |= 1 << (7 - uint(bit%8))
	}
	return raw
}

func todayBit(t *testing.T, day string) int64 {
	t.Helper()
	bit, err := dayOffset(day)
	if err != nil {
		t.Fatal(err)
	}
	return bit
}

func TestDayOffset(t *testing.T) {
	bit, err := dayOffset("2020-01-03")
	if err != nil {
		t.Fatal(err)
	}
	if bit != 2 {
		t.Fatalf("2020-01-03 的偏移 = %d，期望 2", bit)
	}
	if _, err := dayOffset("2026/11/24"); err == nil {
		t.Fatal("非法日期应返回错误")
	}
}

func TestCompute(t *testing.T) {
	cases := []struct {
		name    string
		days    []string
		today   string
		current int
		longest int
		lastDay string
	}{
		{name: "从未签到", today: "2026-11-24"},
		{name: "仅今天", days: []string{"2026-11-24"}, today: "2026-11-24", current: 1, longest: 1, lastDay: "2026-11-24"},
		{name: "仅昨天", days: []string{"2026-11-23"}, today: "2026-11-24", current: 1, longest: 1, lastDay: "2026-11-23"},
		{name: "断档两天", days: []string{"2026-11-21", "2026-11-19", "2026-11-18"}, today: "2026-11-24", current: 0, longest: 2, lastDay: "2026-11-21"},
		{
			name:    "跨月连续",
			days:    []string{"2026-10-30", "2026-10-31", "2026-11-01"},
			today:   "2026-11-01",
			current: 3, longest: 3, lastDay: "2026-11-01",
		},
		{
			name:    "历史最长保留",
			days:    []string{"2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05", "2026-11-24"},
			today:   "2026-11-24",
			current: 1, longest: 5, lastDay: "2026-11-24",
		},
		{
			name:    "连续段跨字节边界",
			days:    []string{"2026-11-05", "2026-11-06", "2026-11-07", "2026-11-08", "2026-11-09", "2026-11-10", "2026-11-11", "2026-11-12", "2026-11-13"},
			today:   "2026-11-24",
			current: 0, longest: 9, lastDay: "2026-11-13",
		},
		{
			name:    "今天与昨天都签到",
			days:    []string{"2026-11-23", "2026-11-24"},
			today:   "2026-11-24",
			current: 2, longest: 2, lastDay: "2026-11-24",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(bitmapOf(t, tc.days...), "de", todayBit(t, tc.today))
			if got.Lang != "de" || got.Current != tc.current || got.Longest != tc.longest {
				t.Fatalf("Compute() = %+v，期望 current=%d longest=%d", got, tc.current, tc.longest)
			}
			if tc.lastDay == "" {
				if got.LastDay != nil {
					t.Fatalf("last_day = %q，期望为空", *got.LastDay)
				}
				return
			}
			if got.LastDay == nil || *got.LastDay != tc.lastDay {
				t.Fatalf("last_day = %v，期望 %s", got.LastDay, tc.lastDay)
			}
		})
	}
}

func TestComputeIgnoresFutureBits(t *testing.T) {
	raw := bitmapOf(t, "2026-11-24", "2026-11-25", "2026-11-26")
	got := Compute(raw, "de", todayBit(t, "2026-11-24"))
	if got.Current != 1 {
		t.Fatalf("current = %d，期望 1", got.Current)
	}
	if got.LastDay == nil || *got.LastDay != "2026-11-24" {
		t.Fatalf("last_day = %v，期望 2026-11-24", got.LastDay)
	}
	if got.Longest != 1 {
		t.Fatalf("longest = %d，期望 1", got.Longest)
	}
}

func TestComputeAnchorDay(t *testing.T) {
	got := Compute(bitmapOf(t, anchor.Format("2006-01-02")), "de", todayBit(t, anchor.Format("2006-01-02")))
	if got.Current != 1 || got.Longest != 1 {
		t.Fatalf("anchor 当天结果错误：%+v", got)
	}
	if got.LastDay == nil || *got.LastDay != "2020-01-01" {
		t.Fatalf("last_day = %v，期望 2020-01-01", got.LastDay)
	}
}

func TestAnchorIsUTC(t *testing.T) {
	if anchor.Location() != time.UTC {
		t.Fatalf("anchor 必须是 UTC，实际 %v", anchor.Location())
	}
}
