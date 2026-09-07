package model

import "time"

// Password 记录一个解压密码（按 sort_order 顺序轮询尝试）。
// Enabled=false 表示停用，停用密码在解压轮询时会被跳过。
type Password struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Value     string    `gorm:"size:256;not null" json:"value"`
	SortOrder int       `gorm:"default:0;index" json:"sort_order"`
	Note      string    `gorm:"size:256" json:"note"`
	Enabled   bool      `gorm:"not null;default:true" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Password) TableName() string { return "passwords" }
