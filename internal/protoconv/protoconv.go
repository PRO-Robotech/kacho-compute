// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package protoconv — единственное место конверсии domain-сущностей kacho-compute
// в proto-сообщения. Используется и service-слоем (для Operation.response), и
// handler-слоем (для Get/List) — НЕ два дублирующих конвертера (как в kacho-vpc).
//
// Контракт: `created_at` всегда truncate до секунд (конвенция Kachō по
// timestamp-точности в proto-ответах).
package protoconv

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

func ts(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t.Truncate(time.Second)) }

// Disk конвертирует domain.Disk → computev1.Disk.
func Disk(d *domain.Disk) *computev1.Disk {
	out := &computev1.Disk{
		Id:                  d.ID,
		ProjectId:           d.ProjectID,
		CreatedAt:           ts(d.CreatedAt),
		Name:                d.Name,
		Description:         d.Description,
		Labels:              d.Labels,
		TypeId:              d.TypeID,
		ZoneId:              d.ZoneID,
		Size:                d.Size,
		BlockSize:           d.BlockSize,
		ProductIds:          d.ProductIDs,
		Status:              computev1.Disk_Status(d.Status),
		InstanceIds:         d.InstanceIDs,
		DiskPlacementPolicy: d.DiskPlacementPolicy,
		HardwareGeneration:  d.HardwareGeneration,
		KmsKey:              d.KMSKey,
	}
	switch {
	case d.SourceImageID != "":
		out.Source = &computev1.Disk_SourceImageId{SourceImageId: d.SourceImageID}
	case d.SourceSnapshotID != "":
		out.Source = &computev1.Disk_SourceSnapshotId{SourceSnapshotId: d.SourceSnapshotID}
	}
	return out
}

// Image конвертирует domain.Image → computev1.Image.
func Image(i *domain.Image) *computev1.Image {
	out := &computev1.Image{
		Id:                 i.ID,
		ProjectId:          i.ProjectID,
		CreatedAt:          ts(i.CreatedAt),
		Name:               i.Name,
		Description:        i.Description,
		Labels:             i.Labels,
		Family:             i.Family,
		StorageSize:        i.StorageSize,
		MinDiskSize:        i.MinDiskSize,
		ProductIds:         i.ProductIDs,
		Status:             computev1.Image_Status(i.Status),
		Pooled:             i.Pooled,
		HardwareGeneration: i.HardwareGeneration,
		KmsKey:             i.KMSKey,
	}
	if i.OsType != domain.OsTypeUnspecified || i.OsNvidiaDriver != "" {
		os := &computev1.Os{Type: computev1.Os_Type(i.OsType)}
		if i.OsNvidiaDriver != "" {
			os.Nvidia = &computev1.Nvidia{Driver: i.OsNvidiaDriver}
		}
		out.Os = os
	}
	return out
}

// Snapshot конвертирует domain.Snapshot → computev1.Snapshot.
func Snapshot(s *domain.Snapshot) *computev1.Snapshot {
	return &computev1.Snapshot{
		Id:                 s.ID,
		ProjectId:          s.ProjectID,
		CreatedAt:          ts(s.CreatedAt),
		Name:               s.Name,
		Description:        s.Description,
		Labels:             s.Labels,
		StorageSize:        s.StorageSize,
		DiskSize:           s.DiskSize,
		ProductIds:         s.ProductIDs,
		Status:             computev1.Snapshot_Status(s.Status),
		SourceDiskId:       s.SourceDiskID,
		HardwareGeneration: s.HardwareGeneration,
		KmsKey:             s.KMSKey,
	}
}

// DiskType конвертирует domain.DiskType → computev1.DiskType.
func DiskType(t *domain.DiskType) *computev1.DiskType {
	return &computev1.DiskType{
		Id:          t.ID,
		Description: t.Description,
		ZoneIds:     t.ZoneIDs,
	}
}

// Instance конвертирует domain.Instance → computev1.Instance.
func Instance(in *domain.Instance) *computev1.Instance {
	out := &computev1.Instance{
		Id:          in.ID,
		ProjectId:   in.ProjectID,
		CreatedAt:   ts(in.CreatedAt),
		Name:        in.Name,
		Description: in.Description,
		Labels:      in.Labels,
		ZoneId:      in.ZoneID,
		PlatformId:  in.PlatformID,
		Resources: &computev1.Resources{
			Memory:       in.Memory,
			Cores:        in.Cores,
			CoreFraction: in.CoreFraction,
			Gpus:         in.GPUs,
		},
		Status:                 computev1.Instance_Status(in.Status),
		Metadata:               in.Metadata,
		MetadataOptions:        in.MetadataOptions,
		ServiceAccountId:       in.ServiceAccountID,
		Fqdn:                   in.FQDN,
		PlacementPolicy:        in.PlacementPolicy,
		HardwareGeneration:     in.HardwareGeneration,
		ReservedInstancePoolId: in.ReservedInstancePoolID,
		Application:            in.Application,
	}
	if in.NetworkSettingsType != "" {
		out.NetworkSettings = &computev1.NetworkSettings{
			Type: networkSettingsTypeFromString(in.NetworkSettingsType),
		}
	}
	if in.SchedulingPreemptible {
		out.SchedulingPolicy = &computev1.SchedulingPolicy{Preemptible: true}
	}
	if boot := in.BootDisk(); boot != nil {
		out.BootDisk = attachedDisk(boot)
	}
	for i := range in.AttachedDisks {
		ad := &in.AttachedDisks[i]
		if ad.IsBoot {
			continue
		}
		out.SecondaryDisks = append(out.SecondaryDisks, attachedDisk(ad))
	}
	for i := range in.NetworkInterfaces {
		out.NetworkInterfaces = append(out.NetworkInterfaces, networkInterface(&in.NetworkInterfaces[i]))
	}
	return out
}

func attachedDisk(ad *domain.AttachedDisk) *computev1.AttachedDisk {
	return &computev1.AttachedDisk{
		Mode:       computev1.AttachedDisk_Mode(ad.Mode),
		DeviceName: ad.DeviceName,
		AutoDelete: ad.AutoDelete,
		// disk_id was renamed to volume_id in the storage-split proto. During the
		// strangler transition compute still owns the local disk row, so the local
		// disk id is what is surfaced on the renamed wire field. The storage-volume
		// attach saga (later cutover slice) replaces this with a real vol-id mirror.
		VolumeId: ad.DiskID,
	}
}

func networkInterface(nic *domain.NetworkInterface) *computev1.NetworkInterface {
	out := &computev1.NetworkInterface{
		Index:            nic.Index,
		NicId:            nic.NICID,
		MacAddress:       nic.MACAddress,
		SubnetId:         nic.SubnetID,
		SecurityGroupIds: nic.SecurityGroupIDs,
	}
	if nic.PrimaryV4Address != "" || nic.PrimaryV4Nat != nil {
		out.PrimaryV4Address = &computev1.PrimaryAddress{
			Address:     nic.PrimaryV4Address,
			OneToOneNat: oneToOneNat(nic.PrimaryV4Nat),
		}
	}
	if nic.PrimaryV6Address != "" || nic.PrimaryV6Nat != nil {
		out.PrimaryV6Address = &computev1.PrimaryAddress{
			Address:     nic.PrimaryV6Address,
			OneToOneNat: oneToOneNat(nic.PrimaryV6Nat),
		}
	}
	return out
}

func oneToOneNat(n *domain.OneToOneNat) *computev1.OneToOneNat {
	if n == nil {
		return nil
	}
	return &computev1.OneToOneNat{
		Address:   n.Address,
		IpVersion: computev1.IpVersion(n.IPVersion),
	}
}

func networkSettingsTypeFromString(s string) computev1.NetworkSettings_Type {
	if v, ok := computev1.NetworkSettings_Type_value[s]; ok {
		return computev1.NetworkSettings_Type(v)
	}
	return computev1.NetworkSettings_STANDARD
}
