// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

func awaitOps(t *testing.T, ops *portmock.OpsRepo) { t.Helper(); portmock.AwaitAllOpsDone(t, ops) }

func TestDiskHandler_CRUD(t *testing.T) {
	diskRepo := portmock.NewDiskRepo()
	ops := portmock.NewOpsRepo()
	svc := service.NewDiskService(diskRepo, portmock.NewImageRepo(), portmock.NewSnapshotRepo(),
		portmock.NewDiskTypeRepo(), portmock.NewZoneRegistry(), &portmock.ProjectClient{OK: true}, ops)
	h := NewDiskHandler(svc, nil)
	ctx := context.Background()

	// Create.
	op, err := h.Create(ctx, &computev1.CreateDiskRequest{ProjectId: "f", Name: "d", ZoneId: "ru-central1-a", Size: 4194304})
	require.NoError(t, err)
	require.NotEmpty(t, op.Id)
	awaitOps(t, ops)
	done, _ := ops.Get(ctx, op.Id)
	var disk computev1.Disk
	require.NoError(t, done.Response.UnmarshalTo(&disk))

	// Get.
	got, err := h.Get(ctx, &computev1.GetDiskRequest{DiskId: disk.Id})
	require.NoError(t, err)
	require.Equal(t, "d", got.Name)

	// Get missing → NotFound.
	_, err = h.Get(ctx, &computev1.GetDiskRequest{DiskId: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))

	// Get without id → InvalidArgument.
	_, err = h.Get(ctx, &computev1.GetDiskRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// List.
	list, err := h.List(ctx, &computev1.ListDisksRequest{ProjectId: "f"})
	require.NoError(t, err)
	require.Len(t, list.Disks, 1)

	// Update.
	_, err = h.Update(ctx, &computev1.UpdateDiskRequest{DiskId: disk.Id, Name: "d2", UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}}})
	require.NoError(t, err)
	awaitOps(t, ops)
	got, _ = h.Get(ctx, &computev1.GetDiskRequest{DiskId: disk.Id})
	require.Equal(t, "d2", got.Name)

	// Delete.
	_, err = h.Delete(ctx, &computev1.DeleteDiskRequest{DiskId: disk.Id})
	require.NoError(t, err)
	awaitOps(t, ops)
	_, err = h.Get(ctx, &computev1.GetDiskRequest{DiskId: disk.Id})
	require.Equal(t, codes.NotFound, status.Code(err))

	// ListSnapshotSchedules / access-bindings → Unimplemented (наследуется).
	_, err = h.ListSnapshotSchedules(ctx, &computev1.ListDiskSnapshotSchedulesRequest{DiskId: "x"})
	require.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestCatalogHandler_ReadOnly(t *testing.T) {
	// Region/Zone serving removed — Geography is owned by kacho-geo.
	// DiskType remains a compute-owned read-only catalog.
	dtSvc := service.NewDiskTypeService(portmock.NewDiskTypeRepo("network-ssd"))
	dh := NewDiskTypeHandler(dtSvc)
	ctx := context.Background()

	dt, err := dh.Get(ctx, &computev1.GetDiskTypeRequest{DiskTypeId: "network-ssd"})
	require.NoError(t, err)
	require.Equal(t, "network-ssd", dt.Id)
	dts, err := dh.List(ctx, &computev1.ListDiskTypesRequest{})
	require.NoError(t, err)
	require.Len(t, dts.DiskTypes, 1)
}

func TestInternalCatalogHandler_AdminCRUD(t *testing.T) {
	dtSvc := service.NewDiskTypeService(portmock.NewDiskTypeRepo("network-ssd"))
	h := NewInternalDiskTypeHandler(dtSvc)
	ctx := context.Background()
	created, err := h.Create(ctx, &computev1.CreateDiskTypeRequest{Id: "network-ssd-io-m3", Description: "io-m3"})
	require.NoError(t, err)
	require.Equal(t, "io-m3", created.Description)
	_, err = h.Delete(ctx, &computev1.DeleteDiskTypeRequest{DiskTypeId: "network-ssd-io-m3"})
	require.NoError(t, err)
}

func TestOperationHandler(t *testing.T) {
	ops := portmock.NewOpsRepo()
	h := NewOperationHandler(ops)
	// Owner poll: op principal must match the caller principal in ctx.
	owner := operations.Principal{Type: "user", ID: "usr-A", DisplayName: "test"}
	ctx := operations.WithPrincipal(context.Background(), owner)
	op, err := operations.New("epd", "test op", &computev1.CreateDiskMetadata{DiskId: "epdx"})
	require.NoError(t, err)
	require.NoError(t, ops.CreateWithPrincipal(ctx, op, owner))
	got, err := h.Get(ctx, &operationpb.GetOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	require.Equal(t, op.ID, got.Id)
	_, err = h.Get(ctx, &operationpb.GetOperationRequest{OperationId: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))
}
