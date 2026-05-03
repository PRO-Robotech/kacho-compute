package domain

import "time"

// Instance — сущность виртуальной машины (entity).
// Импортирует только stdlib.
type Instance struct {
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
	RestartedAt       *time.Time

	// Spec
	DisplayName       string
	Description       string
	PlatformID        string
	ZoneID            string
	Resources         *ResourceSpec
	BootDisk          *AttachedDisk
	SecondaryDisks    []*AttachedDisk
	NetworkInterfaces []*NetworkInterface
	SchedulingPolicy  *SchedulingPolicy
	Metadata          map[string]string // user-data
	FQDN              string
	DesiredPowerState DesiredPowerState

	// Status (only written via Internal.UpdateStatus)
	State                  InstanceState
	StateLastTransitionAt  time.Time
	IPs                    *IPs
	StatusFQDN             string
	HostID                 string
	LastRestartCompletedAt *time.Time
	Conditions             []*Condition
	ObservedGeneration     int64
}
