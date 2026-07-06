// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

// TestInstance_SimulateMaintenanceEvent covers the previously-untested public RPC:
// the sync empty-id guard, the async NotFound path on a missing instance, and the
// happy no-op path returning the instance itself.
func TestInstance_SimulateMaintenanceEvent(t *testing.T) {
	t.Run("empty id → InvalidArgument (sync)", func(t *testing.T) {
		svc, _, _, _, _ := newInstanceSvc(t, true)
		_, err := svc.SimulateMaintenanceEvent(context.Background(), "")
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("missing id → op error NotFound (async)", func(t *testing.T) {
		svc, _, _, _, ops := newInstanceSvc(t, true)
		op, err := svc.SimulateMaintenanceEvent(context.Background(), "epdmissing")
		require.NoError(t, err)
		done := portmock.AwaitOpDone(t, ops, op.ID)
		require.NotNil(t, done.Error, "expected op error for a missing instance")
		require.Equal(t, codes.NotFound, codes.Code(done.Error.Code))
	})

	t.Run("happy → done op carrying the instance (no-op)", func(t *testing.T) {
		svc, repo, _, _, ops := newInstanceSvc(t, true)
		seedRunningInstance(repo, domain.InstanceStatusRunning)
		op, err := svc.SimulateMaintenanceEvent(context.Background(), "epdvm1")
		require.NoError(t, err)
		in := instanceFromOp(t, portmock.AwaitOpDone(t, ops, op.ID))
		require.Equal(t, "epdvm1", in.Id)
		require.Equal(t, "RUNNING", in.Status.String(), "no-op must not transition status")
	})
}

// TestInstance_ListOperations covers the previously-untested public RPC: the
// NotFound guard on a missing instance (service does repo.Get before listing)
// and the happy path on an existing one.
func TestInstance_ListOperations(t *testing.T) {
	t.Run("missing instance → NotFound", func(t *testing.T) {
		svc, _, _, _, _ := newInstanceSvc(t, true)
		_, _, err := svc.ListOperations(context.Background(), "epdmissing", Pagination{})
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("existing instance → no error", func(t *testing.T) {
		svc, repo, _, _, _ := newInstanceSvc(t, true)
		seedRunningInstance(repo, domain.InstanceStatusRunning)
		_, _, err := svc.ListOperations(context.Background(), "epdvm1", Pagination{})
		require.NoError(t, err)
	})
}
