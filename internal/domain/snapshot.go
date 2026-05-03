package domain

import "time"

// Snapshot — снапшот диска.
type Snapshot struct {
	UID               string
	FolderID          string
	CloudID           string
	OrganizationID    string
	Name              string
	Labels            map[string]string
	Annotations       map[string]string
	CreationTimestamp time.Time
	ResourceVersion   int64
	Generation        int64
	DeletionTimestamp *time.Time
	Finalizers        []string

	// Spec
	DisplayName string
	Description string
	DiskID      string

	// Status
	State                 SnapshotState
	StateLastTransitionAt time.Time
	ProgressPercent       int32
	ObservedGeneration    int64
}
