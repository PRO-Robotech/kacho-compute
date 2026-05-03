package domain

import "time"

// DiskStatus — статус жизненного цикла блочного диска.
type DiskStatus int32

const (
	DiskStatusUnspecified DiskStatus = 0
	DiskStatusCreating    DiskStatus = 1
	DiskStatusReady       DiskStatus = 2
	DiskStatusAttaching   DiskStatus = 3
	DiskStatusDetaching   DiskStatus = 4
	DiskStatusError       DiskStatus = 5
	DiskStatusDeleting    DiskStatus = 6
)

// Disk — доменная модель блочного диска.
type Disk struct {
	ID                   string
	FolderID             string
	Name                 string
	Description          string
	CreatedAt            time.Time
	Labels               map[string]string
	DiskTypeID           string
	ZoneID               string
	Size                 string // "10Gi", "100Gi"
	ImageID              string
	Status               DiskStatus
	AttachedToInstanceID string
	Generation           int64
	ResourceVersion      string
	ObservedGeneration   int64
	StatusLastTransitionAt time.Time
	DeletedAt            *time.Time
}
