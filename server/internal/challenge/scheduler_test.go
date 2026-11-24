package challenge

import (
	"testing"
	"time"
)

func TestSchedulerNextRunUsesConfiguredTime(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	s := NewScheduler(nil, loc, nil, 23, 45)
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "配置时刻之前",
			now:  time.Date(2026, 11, 24, 8, 0, 0, 0, loc),
			want: time.Date(2026, 11, 24, 23, 45, 0, 0, loc),
		},
		{
			name: "跨午夜",
			now:  time.Date(2026, 11, 24, 23, 59, 0, 0, loc),
			want: time.Date(2026, 11, 25, 23, 45, 0, 0, loc),
		},
		{
			name: "整点不重复执行",
			now:  time.Date(2026, 11, 24, 23, 45, 0, 0, loc),
			want: time.Date(2026, 11, 25, 23, 45, 0, 0, loc),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.nextRun(tc.now); !got.Equal(tc.want) {
				t.Fatalf("nextRun(%s) = %s, want %s", tc.now, got, tc.want)
			}
		})
	}
}
