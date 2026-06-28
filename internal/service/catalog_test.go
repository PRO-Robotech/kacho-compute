// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

func TestDiskType_GetAndList(t *testing.T) {
	svc := NewDiskTypeService(portmock.NewDiskTypeRepo("network-ssd", "network-hdd"))
	t1, err := svc.Get(context.Background(), "network-ssd")
	require.NoError(t, err)
	require.Equal(t, "network-ssd", t1.ID)
	_, err = svc.Get(context.Background(), "unknown")
	require.Equal(t, codes.NotFound, status.Code(err))
	list, _, err := svc.List(context.Background(), Pagination{})
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestDiskType_AdminCRUD(t *testing.T) {
	svc := NewDiskTypeService(portmock.NewDiskTypeRepo())
	created, err := svc.Create(context.Background(), "network-ssd-io-m3", "io-m3", []string{"ru-central1-a"})
	require.NoError(t, err)
	require.Equal(t, "io-m3", created.Description)
	_, err = svc.Create(context.Background(), "network-ssd-io-m3", "dup", nil)
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	updated, err := svc.Update(context.Background(), "network-ssd-io-m3", "io-m3 v2", []string{"ru-central1-b"})
	require.NoError(t, err)
	require.Equal(t, "io-m3 v2", updated.Description)
	require.NoError(t, svc.Delete(context.Background(), "network-ssd-io-m3"))
	require.Equal(t, codes.NotFound, status.Code(svc.Delete(context.Background(), "network-ssd-io-m3")))
}

// Region/Zone serving (ZoneService/RegionService) removed — Geography
// (Region/Zone) is owned by kacho-geo. zone_id validation goes through the geo
// client (service.ZoneRegistry), exercised in disk_test.go / instance_test.go.
