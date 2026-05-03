package domain

import "time"

// SnapshotStatus — статус жизненного цикла снимка диска.
type SnapshotStatus int32

const (
	SnapshotStatusUnspecified SnapshotStatus = 0
	SnapshotStatusCreating    SnapshotStatus = 1
	SnapshotStatusReady       SnapshotStatus = 2
	SnapshotStatusError       SnapshotStatus = 3
	SnapshotStatusDeleting    SnapshotStatus = 4
)

// Snapshot — доменная модель снимка диска.
type Snapshot struct {
	ID                 string
	FolderID           string
	Name               string
	Description        string
	CreatedAt          time.Time
	Labels             map[string]string
	DiskID             string
	Size               int64
	Status             SnapshotStatus
	ProgressPercent    int32
	Generation         int64
	ResourceVersion    string
	ObservedGeneration int64
	DeletedAt          *time.Time
}
