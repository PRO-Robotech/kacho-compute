package repo

import (
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// DomainInstanceToProto конвертирует domain.Instance в proto для внешнего использования.
func DomainInstanceToProto(inst *domain.Instance) *computev1.Instance {
	return domainInstanceToProto(inst)
}

// DomainDiskToProto конвертирует domain.Disk в proto для внешнего использования.
func DomainDiskToProto(disk *domain.Disk) *computev1.Disk {
	return domainDiskToProto(disk)
}

// DomainSnapshotToProto конвертирует domain.Snapshot в proto для внешнего использования.
func DomainSnapshotToProto(snap *domain.Snapshot) *computev1.Snapshot {
	return domainSnapshotToProto(snap)
}

// ProtoDiskToDomain конвертирует proto Disk в domain.Disk.
func ProtoDiskToDomain(in *computev1.Disk) *domain.Disk {
	if in == nil {
		return nil
	}
	meta := in.GetMetadata()
	spec := in.GetSpec()
	disk := &domain.Disk{}
	if meta != nil {
		disk.UID = meta.GetUid()
		disk.Name = meta.GetName()
		disk.FolderID = meta.GetFolderId()
		disk.CloudID = meta.GetCloudId()
		disk.OrganizationID = meta.GetOrganizationId()
		disk.Labels = meta.GetLabels()
		disk.Annotations = meta.GetAnnotations()
		disk.Finalizers = meta.GetFinalizers()
	}
	if spec != nil {
		disk.DisplayName = spec.GetDisplayName()
		disk.Description = spec.GetDescription()
		disk.DiskTypeID = spec.GetDiskTypeId()
		disk.ZoneID = spec.GetZoneId()
		disk.Size = spec.GetSize()
		disk.ImageID = spec.GetImageId()
	}
	return disk
}

// ProtoSnapshotToDomain конвертирует proto Snapshot в domain.Snapshot.
func ProtoSnapshotToDomain(in *computev1.Snapshot) *domain.Snapshot {
	if in == nil {
		return nil
	}
	meta := in.GetMetadata()
	spec := in.GetSpec()
	snap := &domain.Snapshot{}
	if meta != nil {
		snap.UID = meta.GetUid()
		snap.Name = meta.GetName()
		snap.FolderID = meta.GetFolderId()
		snap.CloudID = meta.GetCloudId()
		snap.OrganizationID = meta.GetOrganizationId()
		snap.Labels = meta.GetLabels()
		snap.Annotations = meta.GetAnnotations()
		snap.Finalizers = meta.GetFinalizers()
	}
	if spec != nil {
		snap.DisplayName = spec.GetDisplayName()
		snap.Description = spec.GetDescription()
		snap.DiskID = spec.GetDiskId()
	}
	return snap
}

// ProtoInstanceToDomain конвертирует proto Instance в domain.Instance.
func ProtoInstanceToDomain(in *computev1.Instance) *domain.Instance {
	if in == nil {
		return nil
	}
	meta := in.GetMetadata()
	spec := in.GetSpec()
	inst := &domain.Instance{}
	if meta != nil {
		inst.UID = meta.GetUid()
		inst.Name = meta.GetName()
		inst.FolderID = meta.GetFolderId()
		inst.CloudID = meta.GetCloudId()
		inst.OrganizationID = meta.GetOrganizationId()
		inst.Labels = meta.GetLabels()
		inst.Annotations = meta.GetAnnotations()
		inst.Finalizers = meta.GetFinalizers()
	}
	if spec != nil {
		inst.DisplayName = spec.GetDisplayName()
		inst.Description = spec.GetDescription()
		inst.PlatformID = spec.GetPlatformId()
		inst.ZoneID = spec.GetZoneId()
		inst.FQDN = spec.GetFqdn()
		inst.DesiredPowerState = domain.DesiredPowerState(spec.GetDesiredPowerState())
		inst.Metadata = spec.GetMetadata()
		if r := spec.GetResources(); r != nil {
			inst.Resources = &domain.ResourceSpec{
				Cores:        r.GetCores(),
				Memory:       r.GetMemory(),
				CoreFraction: r.GetCoreFraction(),
			}
		}
		if bd := spec.GetBootDisk(); bd != nil {
			inst.BootDisk = &domain.AttachedDisk{
				DiskID:     bd.GetDiskId(),
				DeviceName: bd.GetDeviceName(),
				AutoDelete: bd.GetAutoDelete(),
			}
		}
		for _, sd := range spec.GetSecondaryDisks() {
			inst.SecondaryDisks = append(inst.SecondaryDisks, &domain.AttachedDisk{
				DiskID:     sd.GetDiskId(),
				DeviceName: sd.GetDeviceName(),
				AutoDelete: sd.GetAutoDelete(),
			})
		}
		for _, ni := range spec.GetNetworkInterfaces() {
			dni := &domain.NetworkInterface{
				SubnetID:         ni.GetSubnetId(),
				SecurityGroupIDs: ni.GetSecurityGroupIds(),
			}
			if pv4 := ni.GetPrimaryV4Address(); pv4 != nil {
				dni.PrimaryV4Address = &domain.PrimaryV4Address{
					Address: pv4.GetAddress(),
				}
			}
			inst.NetworkInterfaces = append(inst.NetworkInterfaces, dni)
		}
		if sp := spec.GetSchedulingPolicy(); sp != nil {
			inst.SchedulingPolicy = &domain.SchedulingPolicy{
				Preemptible: sp.GetPreemptible(),
			}
		}
	}
	return inst
}
