// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package clients_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	geov1 "github.com/PRO-Robotech/kacho-geo/proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// fakeGeoZoneCli — geov1.ZoneServiceClient под instance-use-case-тест (geo-validate).
type fakeGeoZoneCli struct {
	get func(context.Context, *geov1.GetZoneRequest) (*geov1.Zone, error)
}

func (f fakeGeoZoneCli) Get(ctx context.Context, in *geov1.GetZoneRequest, _ ...grpc.CallOption) (*geov1.Zone, error) {
	return f.get(ctx, in)
}

func (f fakeGeoZoneCli) List(_ context.Context, _ *geov1.ListZonesRequest, _ ...grpc.CallOption) (*geov1.ListZonesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used")
}

func newInstanceSvcGeo(t *testing.T, geoZones fakeGeoZoneCli) (*service.InstanceService, *portmock.OpsRepo) {
	t.Helper()
	diskRepo := portmock.NewDiskRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	geoReg := clients.NewGeoClientWith(geoZones) // GeoClient implements service.ZoneRegistry
	svc := service.NewInstanceService(
		instanceRepo, diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(),
		geoReg, &portmock.ProjectClient{OK: true}, &portmock.VPCClient{AddrFound: true}, ops, false,
	)
	return svc, ops
}

func geoInstanceReq() service.CreateInstanceReq {
	return service.CreateInstanceReq{
		ProjectID: "f", Name: "vm-geo", ZoneID: "ru-central1-a", PlatformID: "standard-v3",
		Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		BootDisk: service.DiskSourceSpec{NewDiskSizeGiB: 4194304, NewSourceImage: ""},
	}
}

// TestInstance_Create_ValidatesZoneViaGeo_OK — S4: Instance.zone_id валидируется
// через kacho-geo (geo client как ZoneRegistry), а не через локальную таблицу.
// Известная geo-зона → Create успешен.
func TestInstance_Create_ValidatesZoneViaGeo_OK(t *testing.T) {
	called := false
	svc, ops := newInstanceSvcGeo(t, fakeGeoZoneCli{get: func(_ context.Context, in *geov1.GetZoneRequest) (*geov1.Zone, error) {
		called = true
		require.Equal(t, "ru-central1-a", in.GetZoneId())
		return &geov1.Zone{Id: "ru-central1-a", RegionId: "ru-central1", Status: geov1.Zone_UP}, nil
	}})

	op, err := svc.Create(context.Background(), geoInstanceReq())
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error, "create with known geo zone must succeed: %v", done.Error)
	require.True(t, called, "Instance.Create must validate zone_id by calling kacho-geo ZoneService.Get")
}

// TestInstance_Create_ValidatesZoneViaGeo_NotFound — geo не знает зону →
// InvalidArgument "Zone ... not found" (NOT_FOUND из geo → InvalidArgument).
func TestInstance_Create_ValidatesZoneViaGeo_NotFound(t *testing.T) {
	svc, ops := newInstanceSvcGeo(t, fakeGeoZoneCli{get: func(_ context.Context, _ *geov1.GetZoneRequest) (*geov1.Zone, error) {
		return nil, status.Error(codes.NotFound, "Zone no-such-zone not found")
	}})

	req := geoInstanceReq()
	req.ZoneID = "no-such-zone"
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Contains(t, done.Error.Message, "no-such-zone")
}

// NOTE: geo-down (fail-closed → Unavailable) на уровне Instance.Create НЕ
// тестируется здесь end-to-end: operations.Run выполняет worker в ctx, detached
// от request-deadline (см. corelib operations.worker), а retry.OnUnavailable
// держит 30s-бюджет — такой тест занял бы 30s. Инвариант покрыт быстро на двух
// уровнях: client-level TestGeoClient_GetZone_Unavailable (geo-down не == not-found,
// не == success) + service-level TestMapZoneRefErr_GeoDown_Unavailable
// (non-NotFound geo-ошибка → Unavailable "zone check: ...").

var _ = operations.Operation{}
