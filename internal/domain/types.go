package domain

import "time"

// ResourceSpec — конфигурация CPU/RAM инстанса.
type ResourceSpec struct {
	Cores        int32
	Memory       string // "4Gi"
	CoreFraction int32
}

// AttachedDisk — ссылка на Disk при монтировании к Instance.
type AttachedDisk struct {
	DiskID     string
	DeviceName string
	AutoDelete bool
}

// PrimaryV4Address — основной IPv4 адрес сетевого интерфейса.
type PrimaryV4Address struct {
	Address string // если задан клиентом — используется
}

// NetworkInterface — сетевой интерфейс Instance.
type NetworkInterface struct {
	SubnetID         string
	SecurityGroupIDs []string
	PrimaryV4Address *PrimaryV4Address
}

// SchedulingPolicy — параметры планирования.
type SchedulingPolicy struct {
	Preemptible bool
}

// IPs — IP-адреса инстанса.
type IPs struct {
	Internal string
	External string
}

// Condition — условие в статусе ресурса.
type Condition struct {
	Type               string
	Status             string
	LastTransitionTime time.Time
	Reason             string
	Message            string
}

// DesiredPowerState — желаемое состояние питания.
type DesiredPowerState int32

const (
	DesiredPowerStateUnspecified DesiredPowerState = 0
	DesiredPowerStateRunning     DesiredPowerState = 1
	DesiredPowerStateStopped     DesiredPowerState = 2
)

// InstanceState — жизненный цикл Instance.
type InstanceState int32

const (
	InstanceStateUnspecified  InstanceState = 0
	InstanceStateProvisioning InstanceState = 1
	InstanceStateRunning      InstanceState = 2
	InstanceStateStopping     InstanceState = 3
	InstanceStateStopped      InstanceState = 4
	InstanceStateStarting     InstanceState = 5
	InstanceStateUpdating     InstanceState = 6
	InstanceStateError        InstanceState = 7
	InstanceStateDeleting     InstanceState = 8
)

// DiskState — жизненный цикл Disk.
type DiskState int32

const (
	DiskStateUnspecified DiskState = 0
	DiskStateCreating    DiskState = 1
	DiskStateReady       DiskState = 2
	DiskStateAttaching   DiskState = 3
	DiskStateDetaching   DiskState = 4
	DiskStateError       DiskState = 5
	DiskStateDeleting    DiskState = 6
)

// ImageState — состояние Image (всегда READY для каталожных образов).
type ImageState int32

const (
	ImageStateUnspecified ImageState = 0
	ImageStateReady       ImageState = 1
)

// SnapshotState — жизненный цикл Snapshot.
type SnapshotState int32

const (
	SnapshotStateUnspecified SnapshotState = 0
	SnapshotStateCreating    SnapshotState = 1
	SnapshotStateReady       SnapshotState = 2
	SnapshotStateError       SnapshotState = 3
	SnapshotStateDeleting    SnapshotState = 4
)
