package store

import (
	"context"
	"time"

	"gorm.io/gorm/clause"
)

// WXUser 对应 wx_users 一行；openid 是微信用户在该小程序内的唯一标识。
type WXUser struct {
	ID          int64     `gorm:"primaryKey;column:id" json:"id"`
	OpenID      string    `gorm:"column:openid" json:"openid"`
	Level       string    `gorm:"column:level" json:"level"`
	UnionID     *string   `gorm:"column:unionid" json:"unionid"`
	Nickname    string    `gorm:"column:nickname" json:"nickname"`
	AvatarURL   string    `gorm:"column:avatar_url" json:"avatar_url"`
	TotalStars  int64     `gorm:"column:total_stars" json:"total_stars"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	LastLoginAt time.Time `gorm:"column:last_login_at" json:"last_login_at"`
}

func (WXUser) TableName() string { return "wx_users" }

// UpsertWXUser 以 openid 去重：首次登录插入，再次登录刷新 unionid 与 last_login_at。
// nickname/avatar 非空时才覆盖，避免用户未授权头像昵称时清空已有资料；
// unionid 为空时保留原值（同一用户在开放平台绑定后才有 unionid）。
// PostgreSQL 的 RETURNING 会把最终行（含已有行的 id）回填到 u。
func (s *Store) UpsertWXUser(ctx context.Context, openid string, unionID, nickname, avatar string) (WXUser, error) {
	var unionPtr *string
	if unionID != "" {
		unionPtr = &unionID
	}
	now := time.Now().UTC()
	u := WXUser{
		OpenID:      openid,
		Level:       "A1",
		UnionID:     unionPtr,
		Nickname:    nickname,
		AvatarURL:   avatar,
		CreatedAt:   now,
		LastLoginAt: now,
	}
	updates := map[string]any{
		"unionid":       clause.Expr{SQL: "COALESCE(EXCLUDED.unionid, wx_users.unionid)"},
		"last_login_at": now,
	}
	if nickname != "" {
		updates["nickname"] = nickname
	}
	if avatar != "" {
		updates["avatar_url"] = avatar
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "openid"}},
			DoUpdates: clause.Assignments(updates),
		}).
		Create(&u).Error
	return u, err
}

// GetWXUserByID 按主键读取用户，用于 token 校验后回填用户信息。
func (s *Store) GetWXUserByID(ctx context.Context, id int64) (WXUser, error) {
	var u WXUser
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&u).Error
	return u, err
}

// UpdateWXUserProfile 定向更新昵称或语言等级，不覆盖未提交的资料字段。
func (s *Store) UpdateWXUserProfile(ctx context.Context, id int64, nickname, level *string) (WXUser, error) {
	updates := make(map[string]any, 2)
	if nickname != nil {
		updates["nickname"] = *nickname
	}
	if level != nil {
		updates["level"] = *level
	}
	if err := s.db.WithContext(ctx).Model(&WXUser{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return WXUser{}, err
	}
	return s.GetWXUserByID(ctx, id)
}

// UpdateWXUserAvatar 定向更新头像对象 key，不覆盖其他用户资料。
func (s *Store) UpdateWXUserAvatar(ctx context.Context, id int64, avatarKey string) (WXUser, error) {
	if err := s.db.WithContext(ctx).Model(&WXUser{}).Where("id = ?", id).Update("avatar_url", avatarKey).Error; err != nil {
		return WXUser{}, err
	}
	return s.GetWXUserByID(ctx, id)
}

// GetWXUserByOpenID 按 openid 读取用户，用于判断是否为首次登录。
func (s *Store) GetWXUserByOpenID(ctx context.Context, openid string) (WXUser, error) {
	var u WXUser
	err := s.db.WithContext(ctx).Where("openid = ?", openid).First(&u).Error
	return u, err
}
