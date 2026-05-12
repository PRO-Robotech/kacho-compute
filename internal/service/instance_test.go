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
		&portmock.FolderClient{OK: folderOK}, &portmock.VPCClient{SubnetFound: true, SubnetZone: "", SGFound: true, AddrFound: true}, ops, false)
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
		FolderID: "f", Name: "vm-1", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
		Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		BootDisk: DiskSourceSpec{NewDiskSizeGiB: diskSizeMin, NewSourceImage: ""},
		NICs:     []NICSpec{{SubnetID: "e9bsubnet"}},
	}
}

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
	require.Len(t, in.NetworkInterfaces, 1)
	require.Contains(t, in.Fqdn, ".auto.internal")
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Len(t, stored.AttachedDisks, 1)
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

	noNIC := baseCreateReq()
	noNIC.NICs = nil
	_, err = svc.Create(context.Background(), noNIC)
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

func TestInstance_Create_SubnetNotFound(t *testing.T) {
	diskRepo := portmock.NewDiskRepo()
	imgRepo := portmock.NewImageRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, diskRepo, imgRepo, portmock.NewSnapshotRepo(), portmock.NewZoneRepo(),
		&portmock.FolderClient{OK: true}, &portmock.VPCClient{SubnetFound: false}, ops, false)
	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
}

func TestInstance_Create_SubnetZoneMismatch(t *testing.T) {
	diskRepo := portmock.NewDiskRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(), portmock.NewZoneRepo(),
		&portmock.FolderClient{OK: true}, &portmock.VPCClient{SubnetFound: true, SubnetZone: "ru-central1-b"}, ops, false)
	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
}

func seedRunningInstance(repo *portmock.InstanceRepo, status domain.InstanceStatus) *domain.Instance {
	in := &domain.Instance{
		ID: "epdvm1", FolderID: "f", Name: "vm", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
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
	diskRepo.Seed(&domain.Disk{ID: "epdboot", FolderID: "f", ZoneID: "ru-central1-a", Status: domain.DiskStatusReady})
	diskRepo.Seed(&domain.Disk{ID: "epddata", FolderID: "f", ZoneID: "ru-central1-a", Status: domain.DiskStatusReady})

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

func TestInstance_Move_OK(t *testing.T) {
	svc, repo, _, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	op, err := svc.Move(context.Background(), "epdvm1", "f2")
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, "f2", in.FolderId)
}

func TestInstance_Delete_AutoDeleteDisk(t *testing.T) {
	svc, repo, diskRepo, _, ops := newInstanceSvc(t, true)
	in0 := seedRunningInstance(repo, domain.InstanceStatusRunning)
	in0.AttachedDisks = []domain.AttachedDisk{{DiskID: "epdboot", IsBoot: true, AutoDelete: true}}
	diskRepo.Seed(&domain.Disk{ID: "epdboot", FolderID: "f", Status: domain.DiskStatusReady})
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

// --- real IPAM (kacho-vpc Address allocation) ---

func newInstanceSvcVPC(t *testing.T, vpc *portmock.VPCClient) (*InstanceService, *portmock.InstanceRepo, *portmock.DiskRepo, *portmock.OpsRepo) {
	t.Helper()
	diskRepo := portmock.NewDiskRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(), portmock.NewZoneRepo(),
		&portmock.FolderClient{OK: true}, vpc, ops, false)
	return svc, instanceRepo, diskRepo, ops
}

func TestInstance_Create_AllocatesRealIPs(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SubnetCidrBlocks: []string{"10.123.0.0/24"}, SGFound: true, AddrFound: true,
		InternalIP: "10.123.0.7", ExternalIP: "198.51.100.42"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet", OneToOneNat: &NatSpec{}}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Len(t, in.NetworkInterfaces, 1)
	require.Equal(t, "10.123.0.7", in.NetworkInterfaces[0].PrimaryV4Address.Address)
	require.NotNil(t, in.NetworkInterfaces[0].PrimaryV4Address.OneToOneNat)
	require.Equal(t, "198.51.100.42", in.NetworkInterfaces[0].PrimaryV4Address.OneToOneNat.Address)
	// 2 ephemeral Address resources created (internal + external NAT).
	require.Len(t, vpc.CreatedAddrIDs, 2)
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.NotEmpty(t, stored.NetworkInterfaces[0].PrimaryV4AddressID)
	require.NotNil(t, stored.NetworkInterfaces[0].PrimaryV4Nat)
	require.True(t, stored.NetworkInterfaces[0].PrimaryV4Nat.Ephemeral)
	require.NotEmpty(t, stored.NetworkInterfaces[0].PrimaryV4Nat.AddressID)

	// Delete → both ephemeral Address resources released.
	op, err = svc.Delete(context.Background(), in.Id)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error)
	require.ElementsMatch(t, vpc.CreatedAddrIDs, vpc.DeletedAddrIDs)
}

func TestInstance_Create_ManualInternalIP_NoAddressResource(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SubnetCidrBlocks: []string{"10.123.0.0/24"}, SGFound: true}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet", PrimaryV4Address: "10.123.0.55"}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, "10.123.0.55", in.NetworkInterfaces[0].PrimaryV4Address.Address)
	require.Empty(t, vpc.CreatedAddrIDs) // manual IP — no Address resource
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Empty(t, stored.NetworkInterfaces[0].PrimaryV4AddressID)
}

func TestInstance_Create_ManualInternalIP_OutsideCIDR(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SubnetCidrBlocks: []string{"10.123.0.0/24"}, SGFound: true}
	svc, _, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet", PrimaryV4Address: "192.0.2.5"}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
}

func TestInstance_Create_DiskFailure_ReleasesAllocatedIPs(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SubnetCidrBlocks: []string{"10.123.0.0/24"}, SGFound: true}
	svc, _, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet", OneToOneNat: &NatSpec{}}}
	// Boot disk refers to a non-existent disk → resolveDiskSource fails after
	// NIC IPs were allocated.
	req.BootDisk = DiskSourceSpec{DiskID: "epdmissing"}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.NotEmpty(t, vpc.CreatedAddrIDs)
	require.ElementsMatch(t, vpc.CreatedAddrIDs, vpc.DeletedAddrIDs) // all released
}

func TestInstance_AddRemoveNAT_RealIP(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SGFound: true, ExternalIP: "198.51.100.77"}
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

func TestInstance_Create_MarksEphemeralAddressesInUse(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SubnetCidrBlocks: []string{"10.123.0.0/24"}, SGFound: true, AddrFound: true,
		InternalIP: "10.123.0.7", ExternalIP: "198.51.100.42"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet", OneToOneNat: &NatSpec{}}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	intAddrID := stored.NetworkInterfaces[0].PrimaryV4AddressID
	extAddrID := stored.NetworkInterfaces[0].PrimaryV4Nat.AddressID
	require.NotEmpty(t, intAddrID)
	require.NotEmpty(t, extAddrID)
	// Ephemeral NIC + ephemeral NAT addresses → MarkAddressEphemeralInUse
	// (reserved=false, used=true + compute_instance referrer atomically).
	require.Equal(t, in.Id, vpc.MarkedEphemeral[intAddrID])
	require.Equal(t, in.Id, vpc.MarkedEphemeral[extAddrID])
	// And NOT through plain SetAddressReference — that's only for reserved addresses.
	require.NotContains(t, vpc.SetRefs, intAddrID)
	require.NotContains(t, vpc.SetRefs, extAddrID)

	// Delete → ephemeral addresses deleted (referrer rows go via FK CASCADE in VPC).
	op, err = svc.Delete(context.Background(), in.Id)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	require.ElementsMatch(t, []string{intAddrID, extAddrID}, vpc.DeletedAddrIDs)
}

func TestInstance_Create_ReservedNatAddress_SetsAndClearsReference(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SubnetCidrBlocks: []string{"10.123.0.0/24"}, SGFound: true, AddrFound: true,
		InternalIP: "10.123.0.7", ExternalIP: "198.51.100.99"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	const reservedAddrID = "e9breservedaddr01"
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet", OneToOneNat: &NatSpec{AddressID: reservedAddrID}}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.False(t, stored.NetworkInterfaces[0].PrimaryV4Nat.Ephemeral)
	require.Equal(t, reservedAddrID, stored.NetworkInterfaces[0].PrimaryV4Nat.AddressID)
	// Reserved NAT address: referrer via SetAddressReference (reserved-flag intact),
	// NOT through MarkAddressEphemeralInUse (we don't flip reserved for user addresses).
	require.Equal(t, in.Id, vpc.SetRefs[reservedAddrID])
	require.NotContains(t, vpc.MarkedEphemeral, reservedAddrID)
	// Ephemeral internal NIC address (compute-created) still goes through Mark.
	intAddrID := stored.NetworkInterfaces[0].PrimaryV4AddressID
	require.NotEmpty(t, intAddrID)
	require.Equal(t, in.Id, vpc.MarkedEphemeral[intAddrID])

	// Delete → reserved address NOT deleted, but its referrer is cleared.
	op, err = svc.Delete(context.Background(), in.Id)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	require.NotContains(t, vpc.DeletedAddrIDs, reservedAddrID)
	require.Contains(t, vpc.ClearedRefs, reservedAddrID)
}

func TestInstance_Create_CreatesVPCNetworkInterface(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SubnetCidrBlocks: []string{"10.123.0.0/24"}, SGFound: true, InternalIP: "10.123.0.7"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet"}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Len(t, in.NetworkInterfaces, 1)
	require.NotEmpty(t, in.NetworkInterfaces[0].NicId)
	require.Len(t, vpc.CreatedNICIDs, 1)
	require.Equal(t, vpc.CreatedNICIDs[0], in.NetworkInterfaces[0].NicId)
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Equal(t, vpc.CreatedNICIDs[0], stored.NetworkInterfaces[0].NICID)

	// Delete → the kacho-vpc NIC is detached + deleted.
	op, err = svc.Delete(context.Background(), in.Id)
	require.NoError(t, err)
	require.Nil(t, portmock.AwaitOpDone(t, ops, op.ID).Error)
	require.Contains(t, vpc.DetachedNICIDs, vpc.CreatedNICIDs[0])
	require.Contains(t, vpc.DeletedNICIDs, vpc.CreatedNICIDs[0])
}

func TestInstance_Create_AttachesExistingNICByID(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SGFound: true, NICFound: true, NICSubnetID: "e9bsubnet"}
	svc, repo, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{NicID: "enpexistingnic01"}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
	require.Equal(t, "enpexistingnic01", in.NetworkInterfaces[0].NicId)
	require.Equal(t, "e9bsubnet", in.NetworkInterfaces[0].SubnetId) // denorm mirror
	require.Contains(t, vpc.AttachedNICIDs, "enpexistingnic01")
	require.Empty(t, vpc.CreatedNICIDs) // attached an existing one, not created
	stored, err := repo.Get(context.Background(), in.Id)
	require.NoError(t, err)
	require.Equal(t, "enpexistingnic01", stored.NetworkInterfaces[0].NICID)
}

func TestInstance_Create_AttachExistingNIC_NotFound(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SGFound: true, NICFound: false}
	svc, _, _, ops := newInstanceSvcVPC(t, vpc)
	req := baseCreateReq()
	req.NICs = []NICSpec{{NicID: "enpmissingnic01"}}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
}

func TestInstance_Create_NicSpec_BothSubnetAndNicID(t *testing.T) {
	svc, _, _, _, _ := newInstanceSvc(t, true)
	req := baseCreateReq()
	req.NICs = []NICSpec{{SubnetID: "e9bsubnet", NicID: "enpnic01"}}
	_, err := svc.Create(context.Background(), req)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestInstance_AddRemoveNAT_MarksEphemeralInUseAndDeletes(t *testing.T) {
	vpc := &portmock.VPCClient{SubnetFound: true, SGFound: true, ExternalIP: "198.51.100.77"}
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
	vpc := &portmock.VPCClient{SubnetFound: true, SGFound: true, AddrFound: true, ExternalIP: "198.51.100.88"}
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

// TestInstance_Create_ZoneFromVPCSource — zone_id валидируется через ZoneRegistry
// (в проде — kacho-vpc InternalZoneService; здесь — VPCClient-mock). Неизвестная
// VPC-зона → операция падает с InvalidArgument "Zone ... not found".
func TestInstance_Create_ZoneFromVPCSource(t *testing.T) {
	diskRepo := portmock.NewDiskRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	vpcSource := &portmock.VPCClient{
		SubnetFound: true, SGFound: true, AddrFound: true,
		Zones: map[string]string{"ru-central1-a": "ru-central1"},
	}
	svc := NewInstanceService(instanceRepo, diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(),
		vpcSource, &portmock.FolderClient{OK: true}, vpcSource, ops, false)

	// known vpc zone → success.
	op, err := svc.Create(context.Background(), baseCreateReq())
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error, "create with known vpc zone must succeed")

	// unknown vpc zone → InvalidArgument.
	bad := baseCreateReq()
	bad.ZoneID = "no-such-zone"
	op2, err := svc.Create(context.Background(), bad)
	require.NoError(t, err)
	done2 := portmock.AwaitOpDone(t, ops, op2.ID)
	require.NotNil(t, done2.Error)
	require.Equal(t, int32(codes.InvalidArgument), done2.Error.Code)
	require.Contains(t, done2.Error.Message, "no-such-zone")
}
