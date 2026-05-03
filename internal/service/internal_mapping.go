package service

import (
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// instanceToProto конвертирует domain.Instance в proto для хранения в Operation.Response.
func instanceToProto(inst *domain.Instance) *computev1.Instance {
	p := &computev1.Instance{
		Id:                 inst.ID,
		FolderId:           inst.FolderID,
		CreatedAt:          timestamppb.New(inst.CreatedAt),
		Name:               inst.Name,
		Description:        inst.Description,
		Labels:             inst.Labels,
		ZoneId:             inst.ZoneID,
		PlatformId:         inst.PlatformID,
		Status:             computev1.Status(inst.Status),
		Fqdn:               inst.FQDN,
		Metadata:           inst.Metadata,
		DesiredPowerState:  computev1.PowerState(inst.DesiredPowerState),
		Generation:         inst.Generation,
		ResourceVersion:    inst.ResourceVersion,
		ObservedGeneration: inst.ObservedGeneration,
		Ips: &computev1.Ips{
			Internal: inst.IPs.Internal,
			External: inst.IPs.External,
		},
	}
	if inst.Resources.Cores > 0 || inst.Resources.Memory != "" {
		p.Resources = &computev1.Resources{
			Cores:        inst.Resources.Cores,
			Memory:       inst.Resources.Memory,
			CoreFraction: inst.Resources.CoreFraction,
			Gpus:         inst.Resources.GPUs,
		}
	}
	return p
}

// diskToProto конвертирует domain.Disk в proto для хранения в Operation.Response.
func diskToProto(d *domain.Disk) *computev1.Disk {
	return &computev1.Disk{
		Id:                   d.ID,
		FolderId:             d.FolderID,
		Name:                 d.Name,
		Description:          d.Description,
		CreatedAt:            timestamppb.New(d.CreatedAt),
		Labels:               d.Labels,
		DiskTypeId:           d.DiskTypeID,
		ZoneId:               d.ZoneID,
		Size:                 d.Size,
		ImageId:              d.ImageID,
		Status:               computev1.DiskStatus(d.Status),
		AttachedToInstanceId: d.AttachedToInstanceID,
		Generation:           d.Generation,
		ResourceVersion:      d.ResourceVersion,
		ObservedGeneration:   d.ObservedGeneration,
	}
}

// snapshotToProto конвертирует domain.Snapshot в proto для хранения в Operation.Response.
func snapshotToProto(s *domain.Snapshot) *computev1.Snapshot {
	return &computev1.Snapshot{
		Id:                 s.ID,
		FolderId:           s.FolderID,
		Name:               s.Name,
		Description:        s.Description,
		CreatedAt:          timestamppb.New(s.CreatedAt),
		Labels:             s.Labels,
		DiskId:             s.DiskID,
		Size:               s.Size,
		Status:             computev1.SnapshotStatus(s.Status),
		ProgressPercent:    s.ProgressPercent,
		Generation:         s.Generation,
		ResourceVersion:    s.ResourceVersion,
		ObservedGeneration: s.ObservedGeneration,
	}
}
