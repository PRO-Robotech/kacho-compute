// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

func newInstanceSvc(t *testing.T, folderOK bool) (*InstanceService, *portmock.InstanceRepo, *portmock.DiskRepo, *portmock.ImageRepo, *portmock.OpsRepo) {
	t.Helper()
	diskRepo := portmock.NewDiskRepo()
	imgRepo := portmock.NewImageRepo()
	snapRepo := portmock.NewSnapshotRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, diskRepo, imgRepo, snapRepo, portmock.NewZoneRegistry(),
		&portmock.ProjectClient{OK: folderOK}, ops)
	return svc, instanceRepo, diskRepo, imgRepo, ops
}

func instanceFromOp(t *testing.T, op *operations.Operation) *computev1.Instance {
	t.Helper()
	require.NotNil(t, op.Response, "operation error=%v", op.Error)
	var in computev1.Instance
	require.NoError(t, op.Response.UnmarshalTo(&in))
	return &in
}

func baseCreateReq() CreateInstanceReq {
	return CreateInstanceReq{
		ProjectID: "f", Name: "vm-1", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
		Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		BootDisk: DiskSourceSpec{NewDiskSizeGiB: diskSizeMin, NewSourceImage: ""},
	}
}

// TestInstance_Create_OK — the Instance is created WITHOUT any network
// interface (no auto-NIC). The rest of the lifecycle (boot disk, status,
// operation envelope) is unaffected.
func TestInstance_Create_OK(t *testing.T) {
	svc, repo, diskRepo, _, ops := newInstanceSvc(t, true)
	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	require.Equal(t, "epd", op.ID[:3])
	done := portmock.AwaitOpDone(t, ops, op.ID)
	in := instanceFromOp(t, done)
	require.Equal(t, "vm-1", in.Name)
	require.Equal(t, computev1.Instance_RUNNING, in.Status)
	require.NotNil(t, in.BootDisk)
	// No NIC is created — Instance has no network interfaces.
	require.Empty(t, in.NetworkInterfaces)
	require.Contains(t, in.Fqdn, ".auto.internal")
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Len(t, stored.AttachedDisks, 1)
	require.Empty(t, stored.NetworkInterfaces)
	// inline disk created and attached.
	bootDisk := stored.BootDisk()
	require.NotNil(t, bootDisk)
	_, err = diskRepo.Get(context.Background(), bootDisk.DiskID)
	require.NoError(t, err)
}

// TestInstance_Create_Fqdn_HostnameSuffix_NoForeignCloudToken — Fqdn built from
// an explicit Hostname must use a Kachō-native internal DNS suffix, never a
// foreign-cloud region token (own-product naming non-negotiable: no other-cloud
// names in code or output). This is the public Instance.Fqdn field, serialized
// on every Get/List/Create.
func TestInstance_Create_Fqdn_HostnameSuffix_NoForeignCloudToken(t *testing.T) {
	svc, _, _, _, ops := newInstanceSvc(t, true)
	req := baseCreateReq()
	req.Hostname = "web1"
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	in := instanceFromOp(t, done)
	require.Equal(t, "web1.kacho.internal", in.Fqdn)
	require.NotContains(t, in.Fqdn, "ru-central1", "Fqdn must not leak a foreign-cloud region token")
}

func TestInstance_Create_SyncValidation(t *testing.T) {
	svc, _, _, _, _ := newInstanceSvc(t, true)
	missing := baseCreateReq()
	missing.ZoneID = ""
	_, err := svc.Create(context.Background(), missing)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	badCF := baseCreateReq()
	badCF.CoreFraction = 33
	_, err = svc.Create(context.Background(), badCF)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	bothBoot := baseCreateReq()
	bothBoot.BootDisk = DiskSourceSpec{DiskID: "d1", NewDiskSizeGiB: diskSizeMin}
	_, err = svc.Create(context.Background(), bothBoot)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestInstance_Create_NoNIC — Instance создаётся без network interface
// (no auto-NIC): NIC-привязка вынесена из lifecycle Instance целиком.
func TestInstance_Create_NoNIC(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Empty(t, in.NetworkInterfaces)
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Empty(t, stored.NetworkInterfaces)
}

func seedRunningInstance(repo *portmock.InstanceRepo, status domain.InstanceStatus) *domain.Instance {
	in := &domain.Instance{
		ID: "epdvm1", ProjectID: "f", Name: "vm", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
		Cores: 2, Memory: 2 << 30, CoreFraction: 100, Status: status,
		AttachedDisks: []domain.AttachedDisk{{DiskID: "epdboot", IsBoot: true}},
	}
	repo.Seed(in)
	return in
}

func TestInstance_Stop_Start_Restart_StateMachine(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)

	op, err := svc.Stop(context.Background(), "epdvm1")
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, computev1.Instance_STOPPED, in.Status)

	// Stop again → FailedPrecondition.
	op, err = svc.Stop(context.Background(), "epdvm1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	op, err = svc.Start(context.Background(), "epdvm1")
	require.NoError(t, err)
	in = instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, computev1.Instance_RUNNING, in.Status)

	op, err = svc.Restart(context.Background(), "epdvm1")
	require.NoError(t, err)
	in = instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, computev1.Instance_RUNNING, in.Status)
}

// TestInstance_Restart_SingleCAS_FromStopped — Restart на не-RUNNING инстансе
// отбивается FailedPrecondition. После перехода на одиночный atomic CAS
// RUNNING→RUNNING (без durable промежуточного RESTARTING) прекондишн-контракт
// сохранён: Stop→Restart → FailedPrecondition, а инстанс НЕ застревает в
// RESTARTING (нет bricked-state, из которого Start/Stop/Restart/Attach падали бы
// навсегда). Регрессионный якорь для finding «Restart persists intermediate
// RESTARTING … interruption leaves the instance permanently bricked».
func TestInstance_Restart_SingleCAS_FromStopped(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusStopped)

	op, err := svc.Restart(context.Background(), "epdvm1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	// Инстанс не тронут: остаётся STOPPED (никакого залипшего RESTARTING),
	// последующий Start проходит штатно.
	got, gerr := repo.Get(context.Background(), "epdvm1")
	require.NoError(t, gerr)
	require.Equal(t, domain.InstanceStatusStopped, got.Status)

	op, err = svc.Start(context.Background(), "epdvm1")
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, computev1.Instance_RUNNING, in.Status)
}

func TestInstance_Update_ResourcesRequiresStopped(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	op, err := svc.Update(context.Background(), UpdateInstanceReq{InstanceID: "epdvm1", Cores: 4, Memory: 4 << 30, CoreFraction: 100, UpdateMask: []string{"resources_spec"}})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	// stop then update should work.
	seedRunningInstance(repo, domain.InstanceStatusStopped)
	op, err = svc.Update(context.Background(), UpdateInstanceReq{InstanceID: "epdvm1", Cores: 4, Memory: 4 << 30, CoreFraction: 100, UpdateMask: []string{"resources_spec"}})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, int64(4), in.Resources.Cores)
}

func TestInstance_Update_NameAnyStatus(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	op, err := svc.Update(context.Background(), UpdateInstanceReq{InstanceID: "epdvm1", Name: "renamed", UpdateMask: []string{"name"}})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, "renamed", in.Name)
}

// TestInstance_Beta04_UpdateLabels_EmitsRegister — β-04: Update with "labels" in
// the update-mask makes the use-case ask the repo to emit a fresh FGA register
// intent (label-mirror refresh).
func TestInstance_Beta04_UpdateLabels_EmitsRegister(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	op, err := svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Labels: map[string]string{"env": "prod"}, UpdateMask: []string{"labels"},
	})
	require.NoError(t, err)
	portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, repo.LastUpdateEmitLabels, "repo.Update must have been called")
	require.True(t, *repo.LastUpdateEmitLabels, "labels in mask → emit register intent (β-04)")
}

// TestInstance_Beta04b_UpdateNonLabels_NoRegister — β-04b: Update without "labels"
// in the mask must NOT trigger a register intent.
func TestInstance_Beta04b_UpdateNonLabels_NoRegister(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	op, err := svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Name: "renamed", UpdateMask: []string{"name"},
	})
	require.NoError(t, err)
	portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, repo.LastUpdateEmitLabels, "repo.Update must have been called")
	require.False(t, *repo.LastUpdateEmitLabels, "no labels in mask → no register intent (β-04b)")
}

// TestInstance_Beta04_FullMaskUpdate_EmitsRegister — β-04: an empty update-mask
// (full-object PATCH) applies labels, so the register intent must be emitted.
func TestInstance_Beta04_FullMaskUpdate_EmitsRegister(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	op, err := svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Name: "renamed", Labels: map[string]string{"env": "prod"},
	})
	require.NoError(t, err)
	portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, repo.LastUpdateEmitLabels)
	require.True(t, *repo.LastUpdateEmitLabels, "full-mask PATCH applies labels → emit register intent")
}

func TestInstance_AttachDetachDisk(t *testing.T) {
	svc, repo, diskRepo, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	diskRepo.Seed(&domain.Disk{ID: "epdboot", ProjectID: "f", ZoneID: "ru-central1-a", Status: domain.DiskStatusReady})
	diskRepo.Seed(&domain.Disk{ID: "epddata", ProjectID: "f", ZoneID: "ru-central1-a", Status: domain.DiskStatusReady})

	op, err := svc.AttachDisk(context.Background(), "epdvm1", DiskSourceSpec{DiskID: "epddata", DeviceName: "data0"})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Len(t, in.SecondaryDisks, 1)
	require.Equal(t, "epddata", in.SecondaryDisks[0].DiskId)

	// detach boot → rejected.
	op, err = svc.DetachDisk(context.Background(), "epdvm1", "epdboot", "")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	// detach data disk OK.
	op, err = svc.DetachDisk(context.Background(), "epdvm1", "epddata", "")
	require.NoError(t, err)
	in = instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Empty(t, in.SecondaryDisks)
}

func TestInstance_UpdateMetadata(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	in0 := seedRunningInstance(repo, domain.InstanceStatusRunning)
	in0.Metadata = map[string]string{"a": "1", "b": "2"}
	op, err := svc.UpdateMetadata(context.Background(), "epdvm1", []string{"a"}, map[string]string{"c": "3"})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.NotContains(t, in.Metadata, "a")
	require.Equal(t, "2", in.Metadata["b"])
	require.Equal(t, "3", in.Metadata["c"])
}

func TestInstance_Delete_AutoDeleteDisk(t *testing.T) {
	svc, repo, diskRepo, _, ops := newInstanceSvc(t, true)
	in0 := seedRunningInstance(repo, domain.InstanceStatusRunning)
	in0.AttachedDisks = []domain.AttachedDisk{{DiskID: "epdboot", IsBoot: true, AutoDelete: true}}
	diskRepo.Seed(&domain.Disk{ID: "epdboot", ProjectID: "f", Status: domain.DiskStatusReady})
	diskRepo.SetAttached("epdboot", true)
	op, err := svc.Delete(context.Background(), "epdvm1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error)
	_, err = repo.Get(context.Background(), "epdvm1")
	require.Error(t, err)
	_, err = diskRepo.Get(context.Background(), "epdboot")
	require.Error(t, err) // auto-deleted
}

func TestInstance_GetSerialPortOutput(t *testing.T) {
	svc, repo, _, _, _ := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	out, err := svc.GetSerialPortOutput(context.Background(), "epdvm1")
	require.NoError(t, err)
	require.Contains(t, out, "epdvm1")
}

// TestInstance_Create_ZoneFromSource — zone_id валидируется через ZoneRegistry
// port. В продакшене этот порт реализует clients.GeoClient (kacho-geo
// geo.v1.ZoneService.Get), а не локальная таблица `zones`; здесь use-case
// тестируется port-агностично через mock. Неизвестная зона → операция падает с
// InvalidArgument "Zone ... not found" (geo NOT_FOUND → InvalidArgument).
// End-to-end geo-валидация — internal/clients/geo_client_instance_test.go.
func TestInstance_Create_ZoneFromSource(t *testing.T) {
	diskRepo := portmock.NewDiskRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	zoneSrc := portmock.NewZoneRegistry("ru-central1-a")
	svc := NewInstanceService(instanceRepo, diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(),
		zoneSrc, &portmock.ProjectClient{OK: true}, ops)

	// known zone → success.
	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error, "create with known zone must succeed")

	// unknown zone → InvalidArgument.
	bad := baseCreateReq()
	bad.ZoneID = "no-such-zone"
	op2, err := svc.Create(context.Background(), bad)
	require.NoError(t, err)
	done2 := portmock.AwaitOpDone(t, ops, op2.ID)
	require.NotNil(t, done2.Error)
	require.Equal(t, int32(codes.InvalidArgument), done2.Error.Code)
	require.Contains(t, done2.Error.Message, "no-such-zone")
}
