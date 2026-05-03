package domain

import "time"

// Disk — сущность блочного диска.
type Disk struct {
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
	DiskTypeID  string
	ZoneID      string
	Size        string
	ImageID     string

	// Status
	State                  DiskState
	StateLastTransitionAt  time.Time
	AttachedToInstanceID   string
	DeviceName             string
	ObservedGeneration     int64
}
