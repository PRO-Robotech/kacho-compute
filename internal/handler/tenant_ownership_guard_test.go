// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// crossTenantCtx — TenantCtx, scoped к folder "f-other" (НЕ владелец ресурса,
// который сидируется в "f-owner"). Любой project-scoped single-resource RPC,
// вызванный с этим ctx на ресурс из "f-owner", ДОЛЖЕН вернуть PermissionDenied
// (AssertProjectOwnership).
func crossTenantCtx() context.Context {
	return context.WithValue(context.Background(), tenantCtxKey{},
		TenantCtx{ProjectIDs: map[string]struct{}{"f-other": {}}})
}

const guardOwnerFolder = "f-owner"

// TestTenantOwnershipGuard_CrossTenantDenied — структурный guard за
// per-resource tenant-isolation. Для КАЖДОГО single-resource project-scoped RPC
// всех четырёх ресурсных хендлеров: ресурс сидируется в folder "f-owner",
// вызов идёт с TenantCtx, scoped к "f-other" → ожидается PermissionDenied.
//
// Назначение — не regression на текущее (полное) покрытие AssertProjectOwnership,
// а STRUCTURAL closure «нельзя добавить project-scoped single-resource RPC, забыв
// ownership-guard»: любой новый такой метод ОБЯЗАН быть добавлен в таблицу ниже
// (иначе cross-tenant-leak пройдёт незамеченным). Если метод здесь отсутствует и
// не гейтит ownership — тест краснеет. (audit ARCH: per-resource tenant authz
// enforced by convention → фиксируем guard-таблицей.)
//
// НЕ покрываются здесь (guard на req.ProjectId, не на fetched-resource): Create,
// top-level List, Image.GetLatestByFamily — их project-scope берётся из запроса,
// а не из БД; отдельные негативы у них в *_handler_test.go.
func TestTenantOwnershipGuard_CrossTenantDenied(t *testing.T) {
	other := crossTenantCtx()
	denied := func(t *testing.T, name string, err error) {
		t.Helper()
		assert.Equalf(t, codes.PermissionDenied, status.Code(err),
			"%s: cross-tenant call must be PermissionDenied (AssertProjectOwnership), got err=%v", name, err)
	}

	// ---- DiskService ----
	t.Run("disk", func(t *testing.T) {
		diskRepo := portmock.NewDiskRepo()
		ops := portmock.NewOpsRepo()
		svc := service.NewDiskService(diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(),
			portmock.NewDiskTypeRepo(), portmock.NewZoneRegistry(), &portmock.ProjectClient{OK: true}, ops)
		h := NewDiskHandler(svc, nil)
		diskRepo.Seed(&domain.Disk{ID: "epddisk1", ProjectID: guardOwnerFolder, Name: "d", ZoneID: "ru-central1-a", Size: 4194304, Status: domain.DiskStatusReady})

		_, err := h.Get(other, &computev1.GetDiskRequest{DiskId: "epddisk1"})
		denied(t, "Disk.Get", err)
		_, err = h.Update(other, &computev1.UpdateDiskRequest{DiskId: "epddisk1", Name: "x"})
		denied(t, "Disk.Update", err)
		_, err = h.Delete(other, &computev1.DeleteDiskRequest{DiskId: "epddisk1"})
		denied(t, "Disk.Delete", err)
		_, err = h.Relocate(other, &computev1.RelocateDiskRequest{DiskId: "epddisk1"})
		denied(t, "Disk.Relocate", err)
		_, err = h.ListOperations(other, &computev1.ListDiskOperationsRequest{DiskId: "epddisk1"})
		denied(t, "Disk.ListOperations", err)
	})

	// ---- ImageService ----
	t.Run("image", func(t *testing.T) {
		imgRepo := portmock.NewImageRepo()
		ops := portmock.NewOpsRepo()
		svc := service.NewImageService(imgRepo, portmock.NewDiskRepo(), portmock.NewSnapshotRepo(),
			&portmock.ProjectClient{OK: true}, ops)
		h := NewImageHandler(svc, nil)
		imgRepo.Seed(&domain.Image{ID: "epdimage1", ProjectID: guardOwnerFolder, Name: "i", Status: domain.ImageStatusReady})

		_, err := h.Get(other, &computev1.GetImageRequest{ImageId: "epdimage1"})
		denied(t, "Image.Get", err)
		_, err = h.Update(other, &computev1.UpdateImageRequest{ImageId: "epdimage1", Name: "x"})
		denied(t, "Image.Update", err)
		_, err = h.Delete(other, &computev1.DeleteImageRequest{ImageId: "epdimage1"})
		denied(t, "Image.Delete", err)
		_, err = h.ListOperations(other, &computev1.ListImageOperationsRequest{ImageId: "epdimage1"})
		denied(t, "Image.ListOperations", err)
	})

	// ---- SnapshotService ----
	t.Run("snapshot", func(t *testing.T) {
		snapRepo := portmock.NewSnapshotRepo()
		ops := portmock.NewOpsRepo()
		svc := service.NewSnapshotService(snapRepo, portmock.NewDiskRepo(), &portmock.ProjectClient{OK: true}, ops)
		h := NewSnapshotHandler(svc, nil)
		snapRepo.Seed(&domain.Snapshot{ID: "epdsnap1", ProjectID: guardOwnerFolder, Name: "s", Status: domain.SnapshotStatusReady})

		_, err := h.Get(other, &computev1.GetSnapshotRequest{SnapshotId: "epdsnap1"})
		denied(t, "Snapshot.Get", err)
		_, err = h.Update(other, &computev1.UpdateSnapshotRequest{SnapshotId: "epdsnap1", Name: "x"})
		denied(t, "Snapshot.Update", err)
		_, err = h.Delete(other, &computev1.DeleteSnapshotRequest{SnapshotId: "epdsnap1"})
		denied(t, "Snapshot.Delete", err)
		_, err = h.ListOperations(other, &computev1.ListSnapshotOperationsRequest{SnapshotId: "epdsnap1"})
		denied(t, "Snapshot.ListOperations", err)
	})

	// ---- InstanceService ----
	t.Run("instance", func(t *testing.T) {
		instRepo := portmock.NewInstanceRepo()
		ops := portmock.NewOpsRepo()
		svc := service.NewInstanceService(instRepo, portmock.NewDiskRepo(), portmock.NewImageRepo(),
			portmock.NewSnapshotRepo(), portmock.NewDiskTypeRepo(), portmock.NewZoneRegistry(), &portmock.ProjectClient{OK: true}, portmock.NewNicClient(), ops)
		h := NewInstanceHandler(svc, nil)
		instRepo.Seed(&domain.Instance{
			ID: "epdvm1", ProjectID: guardOwnerFolder, Name: "vm", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
			Cores: 2, Memory: 2 << 30, CoreFraction: 100, Status: domain.InstanceStatusRunning,
			AttachedDisks: []domain.AttachedDisk{{DiskID: "epdboot", IsBoot: true}},
		})

		_, err := h.Get(other, &computev1.GetInstanceRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.Get", err)
		_, err = h.Update(other, &computev1.UpdateInstanceRequest{InstanceId: "epdvm1", Name: "x"})
		denied(t, "Instance.Update", err)
		_, err = h.UpdateMetadata(other, &computev1.UpdateInstanceMetadataRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.UpdateMetadata", err)
		_, err = h.Delete(other, &computev1.DeleteInstanceRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.Delete", err)
		_, err = h.GetSerialPortOutput(other, &computev1.GetInstanceSerialPortOutputRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.GetSerialPortOutput", err)
		_, err = h.Start(other, &computev1.StartInstanceRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.Start", err)
		_, err = h.Stop(other, &computev1.StopInstanceRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.Stop", err)
		_, err = h.Restart(other, &computev1.RestartInstanceRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.Restart", err)
		_, err = h.AttachDisk(other, &computev1.AttachInstanceDiskRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.AttachDisk", err)
		_, err = h.DetachDisk(other, &computev1.DetachInstanceDiskRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.DetachDisk", err)
		_, err = h.SimulateMaintenanceEvent(other, &computev1.SimulateInstanceMaintenanceEventRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.SimulateMaintenanceEvent", err)
		_, err = h.ListOperations(other, &computev1.ListInstanceOperationsRequest{InstanceId: "epdvm1"})
		denied(t, "Instance.ListOperations", err)
	})
}
