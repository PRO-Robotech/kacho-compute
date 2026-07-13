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

// instSvcKit — фикстура use-case InstanceService со всеми fake-портами (storage/vpc
// attach-state внешние — storage-split cutover; compute local attach-state нет).
type instSvcKit struct {
	svc     *InstanceService
	repo    *portmock.InstanceRepo
	storage *portmock.StorageClient
	nic     *portmock.NicClient
	ops     *portmock.OpsRepo
}

func newInstanceSvc(t *testing.T, folderOK bool) instSvcKit {
	t.Helper()
	instanceRepo := portmock.NewInstanceRepo()
	storage := portmock.NewStorageClient()
	nic := portmock.NewNicClient()
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, portmock.NewZoneRegistry(),
		&portmock.ProjectClient{OK: folderOK}, nic, storage, ops)
	return instSvcKit{svc: svc, repo: instanceRepo, storage: storage, nic: nic, ops: ops}
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
	}
}

// TestInstance_Create_OK — Instance создаётся без привязок (storage-split sec.0.3): нет
// boot/secondary-томов, нет NIC. Attach — отдельными сагами на уже существующих томах.
func TestInstance_Create_OK(t *testing.T) {
	k := newInstanceSvc(t, true)
	op, err := k.svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	require.Equal(t, "epd", op.ID[:3])
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, "vm-1", in.Name)
	require.Equal(t, computev1.Instance_RUNNING, in.Status)
	require.Nil(t, in.BootDisk)
	require.Empty(t, in.SecondaryDisks)
	require.Empty(t, in.NetworkInterfaces)
	require.Contains(t, in.Fqdn, ".auto.internal")
	stored, err := k.repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Empty(t, stored.AttachedDisks)
	require.Empty(t, stored.NetworkInterfaces)
}

// TestInstance_Create_Fqdn_HostnameSuffix_NoForeignCloudToken — Fqdn от Hostname —
// нативный Kachō-суффикс, без foreign-cloud region-токена.
func TestInstance_Create_Fqdn_HostnameSuffix_NoForeignCloudToken(t *testing.T) {
	k := newInstanceSvc(t, true)
	req := baseCreateReq()
	req.Hostname = "web1"
	op, err := k.svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, "web1.kacho.internal", in.Fqdn)
	require.NotContains(t, in.Fqdn, "ru-central1", "Fqdn must not leak a foreign-cloud region token")
}

func TestInstance_Create_SyncValidation(t *testing.T) {
	k := newInstanceSvc(t, true)
	missing := baseCreateReq()
	missing.ZoneID = ""
	_, err := k.svc.Create(context.Background(), missing)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	badCF := baseCreateReq()
	badCF.CoreFraction = 33
	_, err = k.svc.Create(context.Background(), badCF)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func seedRunningInstance(repo *portmock.InstanceRepo, st domain.InstanceStatus) *domain.Instance {
	in := &domain.Instance{
		ID: "epdvm1", ProjectID: "f", Name: "vm", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
		Cores: 2, Memory: 2 << 30, CoreFraction: 100, Status: st,
	}
	repo.Seed(in)
	return in
}

func TestInstance_Stop_Start_Restart_StateMachine(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)

	op, err := k.svc.Stop(context.Background(), "epdvm1")
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, computev1.Instance_STOPPED, in.Status)

	op, err = k.svc.Stop(context.Background(), "epdvm1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	op, err = k.svc.Start(context.Background(), "epdvm1")
	require.NoError(t, err)
	in = instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, computev1.Instance_RUNNING, in.Status)

	op, err = k.svc.Restart(context.Background(), "epdvm1")
	require.NoError(t, err)
	in = instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, computev1.Instance_RUNNING, in.Status)
}

func TestInstance_Restart_SingleCAS_FromStopped(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusStopped)

	op, err := k.svc.Restart(context.Background(), "epdvm1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	got, gerr := k.repo.Get(context.Background(), "epdvm1")
	require.NoError(t, gerr)
	require.Equal(t, domain.InstanceStatusStopped, got.Status)
}

func TestInstance_Update_ResourcesRequiresStopped(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.Update(context.Background(), UpdateInstanceReq{InstanceID: "epdvm1", Cores: 4, Memory: 4 << 30, CoreFraction: 100, UpdateMask: []string{"resources_spec"}})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	seedRunningInstance(k.repo, domain.InstanceStatusStopped)
	op, err = k.svc.Update(context.Background(), UpdateInstanceReq{InstanceID: "epdvm1", Cores: 4, Memory: 4 << 30, CoreFraction: 100, UpdateMask: []string{"resources_spec"}})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, int64(4), in.Resources.Cores)
}

func TestInstance_Update_NameAnyStatus(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.Update(context.Background(), UpdateInstanceReq{InstanceID: "epdvm1", Name: "renamed", UpdateMask: []string{"name"}})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Equal(t, "renamed", in.Name)
}

// TestInstance_Update_MirrorFieldsImmutable — S5-02/S5-05: boot_volume/
// secondary_volumes/network_interfaces (output-only зеркала) + zone_id в mask →
// sync InvalidArgument immutable (не принимаются на вход Instance.Update).
func TestInstance_Update_MirrorFieldsImmutable(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	for _, f := range []string{"zone_id", "boot_volume", "secondary_volumes", "network_interfaces"} {
		_, err := k.svc.Update(context.Background(), UpdateInstanceReq{InstanceID: "epdvm1", UpdateMask: []string{f}})
		require.Equal(t, codes.InvalidArgument, status.Code(err), "field %s must be immutable", f)
		require.Contains(t, status.Convert(err).Message(), "immutable after Instance.Create")
	}
}

func TestInstance_Beta04_UpdateLabels_EmitsRegister(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Labels: map[string]string{"env": "prod"}, UpdateMask: []string{"labels"},
	})
	require.NoError(t, err)
	portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, k.repo.LastUpdateEmitLabels)
	require.True(t, *k.repo.LastUpdateEmitLabels, "labels in mask → emit register intent (β-04)")
}

func TestInstance_Beta04b_UpdateNonLabels_NoRegister(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.Update(context.Background(), UpdateInstanceReq{
		InstanceID: "epdvm1", Name: "renamed", UpdateMask: []string{"name"},
	})
	require.NoError(t, err)
	portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, k.repo.LastUpdateEmitLabels)
	require.False(t, *k.repo.LastUpdateEmitLabels, "no labels in mask → no register intent (β-04b)")
}

// ---- S3: AttachDisk / DetachDisk сага → kacho-storage ----

// TestInstance_AttachDisk_Happy — S3-01: happy attach → storage.Attach форвардит
// self-describing payload; зеркало secondaryVolumes несёт vol-id.
func TestInstance_AttachDisk_Happy(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.AttachDisk(context.Background(), "epdvm1", AttachDiskReq{VolumeID: "voldata1", DeviceName: "sdb"})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.Len(t, in.SecondaryDisks, 1)
	require.Equal(t, "voldata1", in.SecondaryDisks[0].VolumeId)
	require.Equal(t, "sdb", in.SecondaryDisks[0].DeviceName)
}

// TestInstance_AttachDisk_MalformedID — S3-02: malformed instance/volume id →
// sync InvalidArgument (до Operation).
func TestInstance_AttachDisk_MalformedID(t *testing.T) {
	k := newInstanceSvc(t, true)
	_, err := k.svc.AttachDisk(context.Background(), "not-an-ins", AttachDiskReq{VolumeID: "voldata1"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "invalid instance id 'not-an-ins'")

	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	_, err = k.svc.AttachDisk(context.Background(), "epdvm1", AttachDiskReq{VolumeID: "bad-vol"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "invalid volume id 'bad-vol'")
}

// TestInstance_AttachDisk_WrongState — S3-03: инстанс не в {RUNNING, STOPPED} →
// Operation error FailedPrecondition; storage не вызван (нет привязки).
func TestInstance_AttachDisk_WrongState(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusProvisioning)
	op, err := k.svc.AttachDisk(context.Background(), "epdvm1", AttachDiskReq{VolumeID: "voldata1"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Contains(t, done.Error.Message, "Instance must be RUNNING or STOPPED")
	atts, _ := k.storage.ListAttachments(context.Background(), []string{"epdvm1"})
	require.Empty(t, atts, "storage must not be called on gate failure")
}

// TestInstance_AttachDisk_StorageDown_FailClosed — S3-05: storage Unavailable →
// Operation error UNAVAILABLE (fail-closed для мутации).
func TestInstance_AttachDisk_StorageDown_FailClosed(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	k.storage.Err = status.Error(codes.Unavailable, "storage service unavailable")
	op, err := k.svc.AttachDisk(context.Background(), "epdvm1", AttachDiskReq{VolumeID: "voldata1"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.Unavailable), done.Error.Code)
}

// TestInstance_AttachDisk_ZoneMismatch — S3-04: storage возвращает FailedPrecondition
// (zone-coherence CAS-промах) — код/текст пробрасываются как контракт владельца.
func TestInstance_AttachDisk_ZoneMismatch(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	k.storage.SeedZoneMismatch("voldata1", "Volume and Instance must be in the same zone")
	op, err := k.svc.AttachDisk(context.Background(), "epdvm1", AttachDiskReq{VolumeID: "voldata1"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Contains(t, done.Error.Message, "Volume and Instance must be in the same zone")
}

// TestInstance_AttachDisk_IdempotentReplay — S3-09: повторный Attach (already-ours)
// → storage идемпотентный OK, ровно одна привязка.
func TestInstance_AttachDisk_IdempotentReplay(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	for i := 0; i < 2; i++ {
		op, err := k.svc.AttachDisk(context.Background(), "epdvm1", AttachDiskReq{VolumeID: "voldata1", DeviceName: "sdb"})
		require.NoError(t, err)
		done := portmock.AwaitOpDone(t, k.ops, op.ID)
		require.Nil(t, done.Error)
	}
	atts, _ := k.storage.ListAttachments(context.Background(), []string{"epdvm1"})
	require.Len(t, atts, 1, "idempotent replay must not duplicate the attachment")
}

// TestInstance_DetachDisk_Idempotent_And_BootGuard — S3-06.
func TestInstance_DetachDisk_Idempotent_And_BootGuard(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	// seed a data + boot attachment on the storage side.
	_, err := k.storage.Attach(context.Background(), VolumeAttachSpec{VolumeID: "voldata1", InstanceID: "epdvm1", DeviceName: "sdb"})
	require.NoError(t, err)
	_, err = k.storage.Attach(context.Background(), VolumeAttachSpec{VolumeID: "volboot1", InstanceID: "epdvm1", IsBoot: true})
	require.NoError(t, err)

	// detach boot → rejected.
	op, err := k.svc.DetachDisk(context.Background(), "epdvm1", "volboot1", "")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Contains(t, done.Error.Message, "boot volume cannot be detached")

	// detach data by volume_id → OK.
	op, err = k.svc.DetachDisk(context.Background(), "epdvm1", "voldata1", "")
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	for _, sd := range in.SecondaryDisks {
		require.NotEqual(t, "voldata1", sd.VolumeId)
	}

	// detach again (already gone) → idempotent no-op done.
	op, err = k.svc.DetachDisk(context.Background(), "epdvm1", "voldata1", "")
	require.NoError(t, err)
	done = portmock.AwaitOpDone(t, k.ops, op.ID)
	require.Nil(t, done.Error)
}

// TestInstance_DetachDisk_OneofExactlyOne — S3-06: ни одного / оба arm → sync InvalidArgument.
func TestInstance_DetachDisk_OneofExactlyOne(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	_, err := k.svc.DetachDisk(context.Background(), "epdvm1", "", "")
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = k.svc.DetachDisk(context.Background(), "epdvm1", "voldata1", "sdb")
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestInstance_UpdateMetadata(t *testing.T) {
	k := newInstanceSvc(t, true)
	in0 := seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	in0.Metadata = map[string]string{"a": "1", "b": "2"}
	op, err := k.svc.UpdateMetadata(context.Background(), "epdvm1", []string{"a"}, map[string]string{"c": "3"})
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, k.ops, op.ID))
	require.NotContains(t, in.Metadata, "a")
	require.Equal(t, "2", in.Metadata["b"])
	require.Equal(t, "3", in.Metadata["c"])
}

// ---- M2: Delete-сага (release NIC + volume, idempotent replay) ----

// TestInstance_Delete_ReleasesNicAndVolume — M2: Delete снимает ВСЕ NIC- и
// volume-привязки (fix go-review NIC-leak), затем удаляет строку инстанса ПОСЛЕДНЕЙ.
func TestInstance_Delete_ReleasesNicAndVolume(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	_, err := k.storage.Attach(context.Background(), VolumeAttachSpec{VolumeID: "voldata1", InstanceID: "epdvm1"})
	require.NoError(t, err)
	_, err = k.nic.Attach(context.Background(), NicAttachSpec{NICID: "nicaaa1", InstanceID: "epdvm1"})
	require.NoError(t, err)

	op, err := k.svc.Delete(context.Background(), "epdvm1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.Nil(t, done.Error)

	// instance gone.
	_, err = k.repo.Get(context.Background(), "epdvm1")
	require.Error(t, err)
	// volume + NIC bindings released.
	vAtts, _ := k.storage.ListAttachments(context.Background(), []string{"epdvm1"})
	require.Empty(t, vAtts, "volume binding must be released on delete")
	nAtts, _ := k.nic.ListByInstance(context.Background(), []string{"epdvm1"})
	require.Empty(t, nAtts, "NIC binding must be released on delete (fix NIC-leak)")
}

// TestInstance_Delete_IdempotentReplay — M2: повтор Delete на уже удалённом инстансе
// → success (crash-replay не осиротит и не падает).
func TestInstance_Delete_IdempotentReplay(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	op, err := k.svc.Delete(context.Background(), "epdvm1")
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, k.ops, op.ID).Error)

	op, err = k.svc.Delete(context.Background(), "epdvm1")
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, k.ops, op.ID).Error, "replay on gone instance must be idempotent success")
}

// TestInstance_Delete_StorageDown_FailClosed — Delete при недоступном storage →
// Operation error UNAVAILABLE; строка инстанса НЕ удаляется (не осиротить привязки).
func TestInstance_Delete_StorageDown_FailClosed(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	k.storage.Err = status.Error(codes.Unavailable, "storage service unavailable")
	op, err := k.svc.Delete(context.Background(), "epdvm1")
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, k.ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.Unavailable), done.Error.Code)
	_, err = k.repo.Get(context.Background(), "epdvm1")
	require.NoError(t, err, "instance row must survive fail-closed delete")
}

// ---- S5: зеркала (volume mirror + graceful-degrade) ----

// TestInstance_Get_VolumeMirror — S5-01: Get несёт volume-зеркало из storage.
func TestInstance_Get_VolumeMirror(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	_, err := k.storage.Attach(context.Background(), VolumeAttachSpec{VolumeID: "voldata1", InstanceID: "epdvm1", DeviceName: "sdb"})
	require.NoError(t, err)
	got, err := k.svc.Get(context.Background(), "epdvm1")
	require.NoError(t, err)
	require.Len(t, got.AttachedDisks, 1)
	require.Equal(t, "voldata1", got.AttachedDisks[0].DiskID)
}

// TestInstance_Get_MirrorGracefulDegrade — S5-01: storage down → Get НЕ падает,
// volume-зеркало опущено (best-effort).
func TestInstance_Get_MirrorGracefulDegrade(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	k.storage.ListErr = status.Error(codes.Unavailable, "storage service unavailable")
	got, err := k.svc.Get(context.Background(), "epdvm1")
	require.NoError(t, err, "Get must not fail when storage is unavailable")
	require.Empty(t, got.AttachedDisks, "mirror omitted on storage unavailability")
}

func TestInstance_GetSerialPortOutput(t *testing.T) {
	k := newInstanceSvc(t, true)
	seedRunningInstance(k.repo, domain.InstanceStatusRunning)
	out, err := k.svc.GetSerialPortOutput(context.Background(), "epdvm1")
	require.NoError(t, err)
	require.Contains(t, out, "epdvm1")
}

// TestInstance_Create_ZoneFromSource — zone_id валидируется через ZoneRegistry port.
func TestInstance_Create_ZoneFromSource(t *testing.T) {
	instanceRepo := portmock.NewInstanceRepo()
	ops := portmock.NewOpsRepo()
	zoneSrc := portmock.NewZoneRegistry("ru-central1-a")
	svc := NewInstanceService(instanceRepo, zoneSrc, &portmock.ProjectClient{OK: true},
		portmock.NewNicClient(), portmock.NewStorageClient(), ops)

	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error, "create with known zone must succeed")

	bad := baseCreateReq()
	bad.ZoneID = "no-such-zone"
	op2, err := svc.Create(context.Background(), bad)
	require.NoError(t, err)
	done2 := portmock.AwaitOpDone(t, ops, op2.ID)
	require.NotNil(t, done2.Error)
	require.Equal(t, int32(codes.InvalidArgument), done2.Error.Code)
	require.Contains(t, done2.Error.Message, "no-such-zone")
}
