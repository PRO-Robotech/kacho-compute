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
	svc := NewInstanceService(instanceRepo, diskRepo, imgRepo, snapRepo, portmock.NewZoneRepo(),
		&portmock.ProjectClient{OK: folderOK}, &portmock.VPCClient{AddrFound: true}, ops, false)
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

// TestInstance_Create_OK — KAC-266: the Instance is created WITHOUT any network
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

// TestInstance_Create_NoNIC_Materialization — KAC-266: even with peer validation
// enabled (skipIPAM=false), Create must not touch the VPC for NIC/address
// allocation. No ephemeral Address resources are created.
func TestInstance_Create_NoNIC_Materialization(t *testing.T) {
	vpc := &portmock.VPCClient{AddrFound: true, ExternalIP: "198.51.100.42"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Empty(t, in.NetworkInterfaces)
	require.Empty(t, vpc.CreatedAddrIDs)
	require.Empty(t, vpc.MarkedEphemeral)
	require.Empty(t, vpc.SetRefs)
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Empty(t, stored.NetworkInterfaces)
}

func seedRunningInstance(repo *portmock.InstanceRepo, status domain.InstanceStatus) *domain.Instance {
	in := &domain.Instance{
		ID: "epdvm1", ProjectID: "f", Name: "vm", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
		Cores: 2, Memory: 2 << 30, CoreFraction: 100, Status: status,
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsubnet", PrimaryV4Address: "10.0.0.10"}},
		AttachedDisks:     []domain.AttachedDisk{{DiskID: "epdboot", IsBoot: true}},
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

// TestInstance_AddRemoveNAT — one-to-one NAT operates on a NIC already present on
// the Instance row (NIC binding itself is out of the Instance Create lifecycle
// after KAC-266; here a NIC is seeded directly into the row).
func TestInstance_AddRemoveNAT(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)

	op, err := svc.AddOneToOneNat(context.Background(), "epdvm1", "0", nil)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.NotNil(t, in.NetworkInterfaces[0].PrimaryV4Address.OneToOneNat)

	// add again → FailedPrecondition.
	op, err = svc.AddOneToOneNat(context.Background(), "epdvm1", "0", nil)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)

	op, err = svc.RemoveOneToOneNat(context.Background(), "epdvm1", "0")
	require.NoError(t, err)
	in = instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Nil(t, in.NetworkInterfaces[0].PrimaryV4Address.OneToOneNat)
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

// --- one-to-one NAT IPAM (kacho-vpc Address allocation) ---

func newInstanceSvcVPC(t *testing.T, vpc *portmock.VPCClient) (*InstanceService, *portmock.InstanceRepo, *portmock.DiskRepo, *portmock.OpsRepo) {
	t.Helper()
	diskRepo := portmock.NewDiskRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(), portmock.NewZoneRepo(),
		&portmock.ProjectClient{OK: true}, vpc, ops, false)
	return svc, instanceRepo, diskRepo, ops
}

// TestInstance_AddRemoveNAT_RealIP — AddOneToOneNat allocates an ephemeral
// external Address via kacho-vpc; RemoveOneToOneNat releases it.
func TestInstance_AddRemoveNAT_RealIP(t *testing.T) {
	vpc := &portmock.VPCClient{ExternalIP: "198.51.100.77"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	seedRunningInstance(repo, domain.InstanceStatusRunning)

	op, err := svc.AddOneToOneNat(context.Background(), "epdvm1", "0", nil)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.NotNil(t, in.NetworkInterfaces[0].PrimaryV4Address.OneToOneNat)
	require.Equal(t, "198.51.100.77", in.NetworkInterfaces[0].PrimaryV4Address.OneToOneNat.Address)
	require.Len(t, vpc.CreatedAddrIDs, 1)

	op, err = svc.RemoveOneToOneNat(context.Background(), "epdvm1", "0")
	require.NoError(t, err)
	in = instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Nil(t, in.NetworkInterfaces[0].PrimaryV4Address.OneToOneNat)
	require.ElementsMatch(t, vpc.CreatedAddrIDs, vpc.DeletedAddrIDs) // ephemeral external Address released
}

func TestInstance_AddRemoveNAT_MarksEphemeralInUseAndDeletes(t *testing.T) {
	vpc := &portmock.VPCClient{ExternalIP: "198.51.100.77"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	seedRunningInstance(repo, domain.InstanceStatusRunning)

	op, err := svc.AddOneToOneNat(context.Background(), "epdvm1", "0", nil)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	require.Len(t, vpc.CreatedAddrIDs, 1)
	ephAddrID := vpc.CreatedAddrIDs[0]
	// Newly-created ephemeral NAT Address → MarkAddressEphemeralInUse (not SetRefs).
	require.Equal(t, "epdvm1", vpc.MarkedEphemeral[ephAddrID])
	require.NotContains(t, vpc.SetRefs, ephAddrID)

	op, err = svc.RemoveOneToOneNat(context.Background(), "epdvm1", "0")
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	require.Contains(t, vpc.DeletedAddrIDs, ephAddrID) // ephemeral → deleted (referrer via CASCADE)
}

func TestInstance_AddNAT_ReservedAddress_SetsReferenceOnly(t *testing.T) {
	const reservedAddrID = "e9breservedaddr02"
	vpc := &portmock.VPCClient{AddrFound: true, ExternalIP: "198.51.100.88"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	seedRunningInstance(repo, domain.InstanceStatusRunning)

	op, err := svc.AddOneToOneNat(context.Background(), "epdvm1", "0", &NatSpec{AddressID: reservedAddrID})
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	// Reserved user address → SetAddressReference (referrer only, reserved intact),
	// NOT MarkAddressEphemeralInUse.
	require.Equal(t, "epdvm1", vpc.SetRefs[reservedAddrID])
	require.NotContains(t, vpc.MarkedEphemeral, reservedAddrID)
	require.Empty(t, vpc.CreatedAddrIDs) // didn't create a new Address — used reserved one

	op, err = svc.RemoveOneToOneNat(context.Background(), "epdvm1", "0")
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	require.NotContains(t, vpc.DeletedAddrIDs, reservedAddrID) // reserved → not deleted
	require.Contains(t, vpc.ClearedRefs, reservedAddrID)       // referrer cleared
}

// TestInstance_Create_ZoneFromSource — zone_id валидируется через ZoneRegistry
// (compute owns Geography). Неизвестная зона → операция падает с InvalidArgument
// "Zone ... not found".
func TestInstance_Create_ZoneFromSource(t *testing.T) {
	diskRepo := portmock.NewDiskRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	zoneSrc := &portmock.VPCClient{
		AddrFound: true,
		Zones:     map[string]string{"ru-central1-a": "ru-central1"},
	}
	svc := NewInstanceService(instanceRepo, diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(),
		zoneSrc, &portmock.ProjectClient{OK: true}, zoneSrc, ops, false)

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
