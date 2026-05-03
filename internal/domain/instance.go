package domain

import "time"

// InstanceStatus — статус жизненного цикла VM.
type InstanceStatus int32

const (
	InstanceStatusUnspecified  InstanceStatus = 0
	InstanceStatusProvisioning InstanceStatus = 1
	InstanceStatusRunning      InstanceStatus = 2
	InstanceStatusStopping     InstanceStatus = 3
	InstanceStatusStopped      InstanceStatus = 4
	InstanceStatusStarting     InstanceStatus = 5
	InstanceStatusError        InstanceStatus = 6
	InstanceStatusDeleting     InstanceStatus = 7
)

// PowerState — желаемое состояние питания.
type PowerState int32

const (
	PowerStateUnspecified PowerState = 0
	PowerStateRunning     PowerState = 1
	PowerStateStopped     PowerState = 2
)

// NetworkInterface — сетевой интерфейс VM.
type NetworkInterface struct {
	SubnetID          string
	PrimaryV4Address  string
	SecurityGroupIDs  []string
}

// Resources — вычислительные ресурсы VM.
type Resources struct {
	Cores        int64
	Memory       string // "4Gi", "8Gi"
	CoreFraction int32
	GPUs         int64
}

// BootDisk — загрузочный диск.
type BootDisk struct {
	DiskID     string
	DeviceName string
	AutoDelete bool
}

// AttachedDisk — дополнительный диск.
type AttachedDisk struct {
	DiskID     string
	DeviceName string
	AutoDelete bool
}

// SchedulingPolicy — политика планирования.
type SchedulingPolicy struct {
	Preemptible bool
}

// Ips — IP-адреса VM.
type Ips struct {
	Internal []string
	External []string
}

// Instance — доменная модель виртуальной машины.
type Instance struct {
	ID                    string
	FolderID              string
	CreatedAt             time.Time
	Name                  string
	Description           string
	Labels                map[string]string
	ZoneID                string
	PlatformID            string
	Resources             Resources
	Status                InstanceStatus
	FQDN                  string
	Metadata              map[string]string
	BootDisk              BootDisk
	SecondaryDisks        []AttachedDisk
	NetworkInterfaces     []NetworkInterface
	ServiceAccountID      string
	SchedulingPolicy      SchedulingPolicy
	DesiredPowerState     PowerState
	Generation            int64
	ResourceVersion       string
	ObservedGeneration    int64
	StatusLastTransitionAt time.Time
	IPs                   Ips
	LastRestartCompletedAt *time.Time
	DeletedAt             *time.Time
}
