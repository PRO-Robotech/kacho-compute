package domain

import (
	"time"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

// DiskStatus — состояние диска (control-plane: всегда READY после Create).
type DiskStatus int

// Значения DiskStatus зеркалят computev1.Disk_Status.
const (
	DiskStatusUnspecified DiskStatus = iota
	DiskStatusCreating
	DiskStatusReady
	DiskStatusError
	DiskStatusDeleting
)

// Disk — диск (zone-level ресурс). source = image|snapshot хранится в
// SourceImageID / SourceSnapshotID (взаимоисключающие; не FK — YC семантика
// допускает удаление source-ресурса). InstanceIDs — output-only, вычисляется
// из attached_disks (см. repo).
//
// Сложные nested-поля (HardwareGeneration, KMSKey, DiskPlacementPolicy)
// хранятся как proto-указатели; repo сериализует их в JSONB через protojson.
type Disk struct {
	ID                  string
	ProjectID            string
	CreatedAt           time.Time
	Name                string
	Description         string
	Labels              map[string]string
	TypeID              string
	ZoneID              string
	Size                int64
	BlockSize           int64
	ProductIDs          []string
	Status              DiskStatus
	SourceImageID       string
	SourceSnapshotID    string
	DiskPlacementPolicy *computev1.DiskPlacementPolicy
	HardwareGeneration  *computev1.HardwareGeneration
	KMSKey              *computev1.KMSKey

	// InstanceIDs — output-only: instance-ы, к которым диск attached.
	InstanceIDs []string
}
