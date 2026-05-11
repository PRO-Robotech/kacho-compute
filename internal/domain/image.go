package domain

import (
	"time"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

// ImageStatus — состояние образа (control-plane: всегда READY после Create).
type ImageStatus int

// Значения ImageStatus зеркалят computev1.Image_Status.
const (
	ImageStatusUnspecified ImageStatus = iota
	ImageStatusCreating
	ImageStatusReady
	ImageStatusError
	ImageStatusDeleting
)

// OsType — тип ОС в образе (зеркалит computev1.Os_Type).
type OsType int

// Значения OsType.
const (
	OsTypeUnspecified OsType = iota
	OsTypeLinux
	OsTypeWindows
)

// Image — образ (folder-level ресурс). family используется в GetLatestByFamily.
// source (откуда создан) — для observability; не FK.
type Image struct {
	ID                 string
	FolderID           string
	CreatedAt          time.Time
	Name               string
	Description        string
	Labels             map[string]string
	Family             string
	StorageSize        int64
	MinDiskSize        int64
	ProductIDs         []string
	Status             ImageStatus
	OsType             OsType
	OsNvidiaDriver     string
	Pooled             bool
	HardwareGeneration *computev1.HardwareGeneration
	KMSKey             *computev1.KMSKey

	// source (откуда создан образ) — observability-поля, не FK.
	SourceImageID    string
	SourceSnapshotID string
	SourceDiskID     string
	SourceURI        string
}
