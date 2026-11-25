// Package streak 用 Redis bitmap 记录每日签到，并计算当前/最长连续天数。
// bitmap 第 0 位对应 anchor 当天，之后每天占一位，key 按语种与 openid 隔离。
package streak

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// anchor 是 bitmap 位偏移 0 对应的日期。
var anchor = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// Streak 是连续签到结果，字段与每日关卡接口保持一致。
type Streak struct {
	Lang    string  `json:"lang"`
	Current int     `json:"current"`
	Longest int     `json:"longest"`
	LastDay *string `json:"last_day"`
}

// Day 是一条历史签到记录，用于回填 bitmap。
type Day struct {
	OpenID string
	Lang   string
	Day    string
}

// Store 封装签到 bitmap 的读写。
type Store struct {
	rdb *redis.Client
}

// New 创建 Redis 客户端；此时不建立连接，由 Ping 或首次读写建立。
func New(addr, password string, db int) *Store {
	return &Store{rdb: redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})}
}

// Ping 检查 Redis 可用性，供服务启动时做依赖检查。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}
	return nil
}

// Close 关闭底层连接。
func (s *Store) Close() error {
	return s.rdb.Close()
}

func streakKey(openid, lang string) string {
	return "streak:" + lang + ":" + openid
}

// dayOffset 把日期转换为 bitmap 位偏移。
func dayOffset(day string) (int64, error) {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0, fmt.Errorf("invalid day %q: %w", day, err)
	}
	return int64(d.Sub(anchor).Hours() / 24), nil
}

// Mark 记录某天已签到；重复写入同一天是幂等的。
func (s *Store) Mark(ctx context.Context, openid, lang, day string) error {
	bit, err := dayOffset(day)
	if err != nil {
		return err
	}
	return s.rdb.SetBit(ctx, streakKey(openid, lang), bit, 1).Err()
}

// Get 读取 bitmap 并计算连续签到；无记录时返回零值。
func (s *Store) Get(ctx context.Context, openid, lang, today string) (Streak, error) {
	todayBit, err := dayOffset(today)
	if err != nil {
		return Streak{Lang: lang}, err
	}
	raw, err := s.rdb.Get(ctx, streakKey(openid, lang)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Streak{Lang: lang}, nil
	}
	if err != nil {
		return Streak{Lang: lang}, err
	}
	return Compute(raw, lang, todayBit), nil
}

// MarkedDays 返回 [from, to] 区间内（含端点）已打卡的日期列表，格式 YYYY-MM-DD，升序。
// 用于打卡日历渲染；无记录时返回空切片。
func (s *Store) MarkedDays(ctx context.Context, openid, lang, from, to string) ([]string, error) {
	fromBit, err := dayOffset(from)
	if err != nil {
		return nil, err
	}
	toBit, err := dayOffset(to)
	if err != nil {
		return nil, err
	}
	if toBit < fromBit {
		return []string{}, nil
	}
	raw, err := s.rdb.Get(ctx, streakKey(openid, lang)).Bytes()
	if errors.Is(err, redis.Nil) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	days := make([]string, 0, 31)
	for bit := fromBit; bit <= toBit; bit++ {
		if bitAt(raw, bit) {
			days = append(days, anchor.AddDate(0, 0, int(bit)).Format("2006-01-02"))
		}
	}
	return days, nil
}

// Backfill 幂等写入历史签到位，避免切换到 bitmap 后丢失已有连续记录。
func (s *Store) Backfill(ctx context.Context, days []Day) (int, error) {
	if len(days) == 0 {
		return 0, nil
	}
	pipe := s.rdb.Pipeline()
	for _, item := range days {
		bit, err := dayOffset(item.Day)
		if err != nil {
			continue
		}
		pipe.SetBit(ctx, streakKey(item.OpenID, item.Lang), bit, 1)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return len(days), nil
}

// Compute 从 bitmap 计算连续签到：
// 只统计今天及更早的位；longest 为全量最长连续段，current 仅在最后签到日为今天或昨天时向前统计。
func Compute(raw []byte, lang string, todayBit int64) Streak {
	out := Streak{Lang: lang}
	limit := todayBit
	if limit > int64(len(raw))*8-1 {
		limit = int64(len(raw))*8 - 1
	}
	longest, run := 0, 0
	last := int64(-1)
	for i := int64(0); i <= limit; i++ {
		if bitAt(raw, i) {
			run++
			if run > longest {
				longest = run
			}
			last = i
		} else {
			run = 0
		}
	}
	out.Longest = longest
	if last < 0 {
		return out
	}
	day := anchor.AddDate(0, 0, int(last)).Format("2006-01-02")
	out.LastDay = &day
	if last == todayBit || last == todayBit-1 {
		for i := last; i >= 0 && bitAt(raw, i); i-- {
			out.Current++
		}
	}
	return out
}

// bitAt 按 Redis SETBIT 的位序读取：偏移 0 是首字节的最高位。
func bitAt(raw []byte, i int64) bool {
	index := i / 8
	if i < 0 || index >= int64(len(raw)) {
		return false
	}
	return raw[index]&(1<<(7-uint(i%8))) != 0
}
