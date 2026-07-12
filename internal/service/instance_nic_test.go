// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

func newNicSvc(t *testing.T) (*InstanceService, *portmock.InstanceRepo, *portmock.NicClient, *portmock.OpsRepo) {
	t.Helper()
	instanceRepo := portmock.NewInstanceRepo()
	nic := portmock.NewNicClient()
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, portmock.NewDiskRepo(), portmock.NewImageRepo(),
		portmock.NewSnapshotRepo(), portmock.NewDiskTypeRepo(), portmock.NewZoneRegistry(),
		&portmock.ProjectClient{OK: true}, nic, ops)
	return svc, instanceRepo, nic, ops
}

func seedInstance(repo *portmock.InstanceRepo, st domain.InstanceStatus) *domain.Instance {
	in := &domain.Instance{
		ID: ids.NewID(ids.PrefixInstance), ProjectID: "p", Name: "vm-1",
		ZoneID: "ru-central1-a", Status: st,
	}
	repo.Seed(in)
	return in
}

// S4-01: несколько NIC на инстанс — happy; авто-index 0 и 1; Get-зеркало = 2.
func TestAttachNIC_HappyMultiNIC(t *testing.T) {
	svc, repo, _, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)
	enp1 := ids.NewID(ids.PrefixNetworkInterface)
	enp2 := ids.NewID(ids.PrefixNetworkInterface)

	op1, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp1, 0)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op1.ID).Error)

	op2, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp2, 0)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op2.ID).Error)

	got, err := svc.Get(context.Background(), in.ID)
	require.NoError(t, err)
	require.Len(t, got.NetworkInterfaces, 2)
	require.Equal(t, "0", got.NetworkInterfaces[0].Index)
	require.Equal(t, enp1, got.NetworkInterfaces[0].NICID)
	require.Equal(t, "1", got.NetworkInterfaces[1].Index)
	require.Equal(t, enp2, got.NetworkInterfaces[1].NICID)
}

// S4-01/A10 concurrency: два конкурентных Attach на один инстанс → разные слоты,
// оба done (fake CAS сериализует под mutex; проверяем детерминированно под -race).
func TestAttachNIC_ConcurrentAutoIndex(t *testing.T) {
	svc, repo, _, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)
	enpA := ids.NewID(ids.PrefixNetworkInterface)
	enpB := ids.NewID(ids.PrefixNetworkInterface)

	var wg sync.WaitGroup
	for _, nic := range []string{enpA, enpB} {
		wg.Add(1)
		go func(nicID string) {
			defer wg.Done()
			op, err := svc.AttachNetworkInterface(context.Background(), in.ID, nicID, 0)
			require.NoError(t, err)
			require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
		}(nic)
	}
	wg.Wait()

	got, err := svc.Get(context.Background(), in.ID)
	require.NoError(t, err)
	require.Len(t, got.NetworkInterfaces, 2)
	// Разные слоты, lost-update нет.
	require.NotEqual(t, got.NetworkInterfaces[0].Index, got.NetworkInterfaces[1].Index)
}

// S4-03: zone-coherence mismatch → Operation error FailedPrecondition, точный текст.
func TestAttachNIC_ZoneCoherence(t *testing.T) {
	svc, repo, nic, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)
	enp := ids.NewID(ids.PrefixNetworkInterface)
	nic.SeedZoneMismatch(enp, "NetworkInterface subnet is in zone Z2, instance zone is Z1")

	op, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp, 0)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Equal(t, "NetworkInterface subnet is in zone Z2, instance zone is Z1", done.Error.Message)
}

// S4-04: NIC занят другим инстансом → FailedPrecondition "NetworkInterface is in use".
func TestAttachNIC_InUse(t *testing.T) {
	svc, repo, nic, ops := newNicSvc(t)
	other := seedInstance(repo, domain.InstanceStatusStopped)
	in := &domain.Instance{ID: ids.NewID(ids.PrefixInstance), ProjectID: "p", Name: "vm-2",
		ZoneID: "ru-central1-a", Status: domain.InstanceStatusStopped}
	repo.Seed(in)
	enp := ids.NewID(ids.PrefixNetworkInterface)
	// Pre-bind enp to `other`.
	_, err := nic.Attach(context.Background(), NicAttachSpec{NICID: enp, InstanceID: other.ID})
	require.NoError(t, err)

	op, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp, 0)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Equal(t, "NetworkInterface is in use", done.Error.Message)
}

// S4-06: malformed enp-id → sync InvalidArgument "invalid network interface id '<X>'".
func TestAttachNIC_MalformedID(t *testing.T) {
	svc, repo, _, _ := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)

	_, err := svc.AttachNetworkInterface(context.Background(), in.ID, "bad-nic", 0)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "invalid network interface id 'bad-nic'", status.Convert(err).Message())
}

// wrong-state: инстанс не RUNNING/STOPPED → FailedPrecondition.
func TestAttachNIC_WrongState(t *testing.T) {
	svc, repo, _, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusProvisioning)
	enp := ids.NewID(ids.PrefixNetworkInterface)

	op, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp, 0)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Equal(t, "Instance is not running or stopped", done.Error.Message)
}

// S4-07: vpc Unavailable → fail-closed; чистый Unavailable (без leak dial-деталей).
func TestAttachNIC_UnavailableFailClosed(t *testing.T) {
	svc, repo, nic, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)
	nic.Err = status.Error(codes.Unavailable, "connection error: desc = transport: dial tcp 10.1.2.3:9091")
	enp := ids.NewID(ids.PrefixNetworkInterface)

	op, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp, 0)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.Unavailable), done.Error.Code)
	require.Equal(t, "network interface service unavailable", done.Error.Message)
	require.NotContains(t, done.Error.Message, "10.1.2.3", "must not leak dial details")
}

// S4-06: DetachNetworkInterface по nic_id + идемпотентный повтор.
func TestDetachNIC_ByID_Idempotent(t *testing.T) {
	svc, repo, nic, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)
	enp := ids.NewID(ids.PrefixNetworkInterface)
	attOp, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp, 0)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, attOp.ID).Error)

	op, err := svc.DetachNetworkInterface(context.Background(), in.ID, enp, 0, false)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	got, err := svc.Get(context.Background(), in.ID)
	require.NoError(t, err)
	require.Empty(t, got.NetworkInterfaces)
	_ = nic

	// Идемпотентный повтор — done, no-op.
	op2, err := svc.DetachNetworkInterface(context.Background(), in.ID, enp, 0, false)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op2.ID).Error)
}

// S4-06: DetachNetworkInterface по alt-arm index (эквивалентно nic_id).
func TestDetachNIC_ByIndex(t *testing.T) {
	svc, repo, _, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)
	enp := ids.NewID(ids.PrefixNetworkInterface)
	attOp, err := svc.AttachNetworkInterface(context.Background(), in.ID, enp, 0)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, attOp.ID).Error)

	op, err := svc.DetachNetworkInterface(context.Background(), in.ID, "", 0, true)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	got, err := svc.Get(context.Background(), in.ID)
	require.NoError(t, err)
	require.Empty(t, got.NetworkInterfaces)
}

// S4-06: detach по index несуществующего слота → done, no-op (идемпотентно).
func TestDetachNIC_ByIndex_NoSlot(t *testing.T) {
	svc, repo, _, ops := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)

	op, err := svc.DetachNetworkInterface(context.Background(), in.ID, "", 7, true)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
}

// S5-01: kacho-vpc недоступен → Instance.Get успешен, NIC-зеркало опущено.
func TestInstanceGet_NicMirrorGracefulDegrade(t *testing.T) {
	svc, repo, nic, _ := newNicSvc(t)
	in := seedInstance(repo, domain.InstanceStatusStopped)
	nic.ListErr = status.Error(codes.Unavailable, "vpc down")

	got, err := svc.Get(context.Background(), in.ID)
	require.NoError(t, err, "Get must not fail when vpc is down")
	require.Empty(t, got.NetworkInterfaces, "mirror omitted on vpc unavailable")
}
