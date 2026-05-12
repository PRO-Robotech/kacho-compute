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

// Region — глобальный geography-ресурс (id = "ru-central1"). Домен kacho-compute
// (перенесено из kacho-vpc, эпик KAC-15).
type Region struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Zone — availability-зона (глобальный read-only справочник; id = "ru-central1-a").
// Принадлежит Region (region_id, FK RESTRICT).
type Zone struct {
	ID        string
	RegionID  string
	Name      string
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
