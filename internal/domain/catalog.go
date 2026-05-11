package domain

import "time"

// ZoneStatus — статус availability-зоны (зеркалит computev1.Zone_Status).
type ZoneStatus int

// Значения ZoneStatus.
const (
	ZoneStatusUnspecified ZoneStatus = iota
	ZoneStatusUp
	ZoneStatusDown
)

// Zone — availability-зона (глобальный read-only справочник; id = "ru-central1-a").
type Zone struct {
	ID        string
	RegionID  string
	Status    ZoneStatus
	CreatedAt time.Time
}

// DiskType — тип диска (глобальный read-only справочник; id = "network-ssd").
type DiskType struct {
	ID          string
	Description string
	ZoneIDs     []string
	CreatedAt   time.Time
}
