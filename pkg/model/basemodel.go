package model

import (
	"time"

	"gorm.io/gorm"
)

// BaseModel is an alternative to gorm.Model if a model needs to declare ID as UUID instead of uint
type BaseModel struct {
	ID        string         `gorm:"primarykey;type:uuid;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
