package domain

import "time"

// ImageStatus — статус образа диска.
type ImageStatus int32

const (
	ImageStatusUnspecified ImageStatus = 0
	ImageStatusReady       ImageStatus = 1
)

// Image — доменная модель образа диска (read-only catalog).
type Image struct {
	ID          string
	Name        string
	Description string
	Family      string
	OsType      string
	Size        int64
	Status      ImageStatus
}

// CreatedAt — образы не хранят created_at, возвращаем zero time для совместимости курсора.
func (img *Image) CreatedAt() time.Time { return time.Time{} }
