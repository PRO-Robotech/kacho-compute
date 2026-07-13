// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import (
	"errors"
	"time"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

// MaxCPUGuaranteePercent — верхняя граница cpu_guarantee_percent (нижняя — 0).
const MaxCPUGuaranteePercent = 100

// ErrInvalidCPUGuaranteePercent — cpu_guarantee_percent вне допустимого [0,100].
var ErrInvalidCPUGuaranteePercent = errors.New("cpu_guarantee_percent out of range")

// ValidCPUGuaranteePercent сообщает, лежит ли v в допустимом диапазоне [0,100]
// (0 = best-effort/burstable, 1..100 = гарантированный baseline per vCPU).
func ValidCPUGuaranteePercent(v int32) bool { return v >= 0 && v <= MaxCPUGuaranteePercent }

// InstanceStatus — состояние ВМ (control-plane: детерминированная state-машина).
// Значения зеркалят computev1.Instance_Status.
type InstanceStatus int

// Значения InstanceStatus.
const (
	InstanceStatusUnspecified InstanceStatus = iota
	InstanceStatusProvisioning
	InstanceStatusRunning
	InstanceStatusStopping
	InstanceStatusStopped
	InstanceStatusStarting
	InstanceStatusRestarting
	InstanceStatusUpdating
	InstanceStatusError
	InstanceStatusCrashed
	InstanceStatusDeleting
)

// AttachedDiskMode — режим подключения диска (зеркалит computev1.AttachedDisk_Mode).
type AttachedDiskMode int

// Значения AttachedDiskMode.
const (
	AttachedDiskModeUnspecified AttachedDiskMode = iota
	AttachedDiskModeReadOnly
	AttachedDiskModeReadWrite
)

// AttachedDisk — строка таблицы attached_disks (instance ↔ disk M:N).
type AttachedDisk struct {
	DiskID     string
	IsBoot     bool
	Mode       AttachedDiskMode
	DeviceName string
	AutoDelete bool
	AttachedAt time.Time
}

// OneToOneNat — конфигурация one-to-one NAT на NIC. `Address` — реальный
// внешний IPv4 (выделен из AddressPool через kacho-vpc IPAM), `AddressID` — id
// соответствующего VPC Address-ресурса. `Ephemeral` = true если этот Address
// был создан compute'ом для данного NIC (значит compute обязан удалить его при
// teardown); false — если клиент передал ссылку на свой reserved Address (или
// если IP синтетический в SKIP_PEER_VALIDATION-режиме). Хранится как JSONB в
// primary_v4_nat / primary_v6_nat.
type OneToOneNat struct {
	Address    string `json:"address,omitempty"`
	AddressID  string `json:"address_id,omitempty"`
	Ephemeral  bool   `json:"ephemeral,omitempty"`
	IPVersion  int32  `json:"ip_version,omitempty"`
	DNSRecords []byte `json:"dns_records,omitempty"`
}

// NetworkInterface — строка таблицы instance_network_interfaces (cascade child).
//
// PrimaryV4Address — реальный внутренний IPv4 из CIDR подсети (выделен через
// kacho-vpc IPAM, либо задан клиентом вручную). PrimaryV4AddressID — id
// VPC Address-ресурса, который compute создал для авто-аллокации этого IP;
// "" если IP задан клиентом вручную (тогда Address-ресурс не создаётся) либо
// если IP синтетический (SKIP_PEER_VALIDATION). Непустой PrimaryV4AddressID
// означает «эфемерный» Address — compute удалит его при teardown.
// NICID — id связанного kacho-vpc NetworkInterface-ресурса
// (vpc.NetworkInterface). Пусто для legacy-NIC и в SKIP_PEER_VALIDATION
// (синтетический NIC без vpc-ресурса). Поля SubnetID/PrimaryV4Address/
// SecurityGroupIDs становятся read-only denormalised mirror NIC-ресурса (source of
// truth = kacho-vpc) когда NICID непуст.
type NetworkInterface struct {
	Index              string
	NICID              string
	MACAddress         string
	SubnetID           string
	PrimaryV4Address   string
	PrimaryV4AddressID string
	PrimaryV4Nat       *OneToOneNat
	PrimaryV6Address   string
	PrimaryV6Nat       *OneToOneNat
	SecurityGroupIDs   []string
}

// Instance — виртуальная машина (folder-level ресурс).
//
// NetworkInterfaces / AttachedDisks загружаются join-ом из дочерних таблиц.
// Сложные nested-поля (MetadataOptions, PlacementPolicy, HardwareGeneration,
// Application) хранятся как proto-указатели; repo сериализует их в JSONB.
type Instance struct {
	ID                            string
	ProjectID                     string
	CreatedAt                     time.Time
	Name                          string
	Description                   string
	Labels                        map[string]string
	ZoneID                        string
	PlatformID                    string
	Cores                         int64
	Memory                        int64
	CoreFraction                  int64
	GPUs                          int64
	CPUGuaranteePercent           int32
	Image                         string
	ImageDigest                   string
	Status                        InstanceStatus
	Metadata                      map[string]string
	MetadataOptions               *computev1.MetadataOptions
	ServiceAccountID              string
	Hostname                      string
	FQDN                          string
	NetworkSettingsType           string
	SchedulingPreemptible         bool
	PlacementPolicy               *computev1.PlacementPolicy
	SerialPortSSHAuthorization    string
	GPUClusterID                  string
	HardwareGeneration            *computev1.HardwareGeneration
	MaintenancePolicy             string
	MaintenanceGracePeriodSeconds int64
	ReservedInstancePoolID        string
	HostGroupID                   string
	HostID                        string
	Application                   *computev1.Application

	NetworkInterfaces []NetworkInterface
	AttachedDisks     []AttachedDisk
}

// Validate проверяет доменные инварианты Instance (self-validating domain):
// cpu_guarantee_percent обязан лежать в [0,100] (зеркалит DB-CHECK). Вызывается на
// persistence-границе use-case'а как last-line guard инварианта.
func (i *Instance) Validate() error {
	if !ValidCPUGuaranteePercent(i.CPUGuaranteePercent) {
		return ErrInvalidCPUGuaranteePercent
	}
	return nil
}

// BootDisk возвращает boot attached-disk (is_boot=true) или nil.
func (i *Instance) BootDisk() *AttachedDisk {
	for idx := range i.AttachedDisks {
		if i.AttachedDisks[idx].IsBoot {
			return &i.AttachedDisks[idx]
		}
	}
	return nil
}
