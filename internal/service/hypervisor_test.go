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

func TestHypervisor_RegisterGetListUpdateDeregister(t *testing.T) {
	ctx := context.Background()
	svc := NewHypervisorService(portmock.NewHypervisorRepo())

	// empty zone_id -> InvalidArgument
	_, err := svc.Register(ctx, "", "", "", domain.HypervisorCapacity{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// register two -> distinct node_index (0, 1)
	a, err := svc.Register(ctx, "hyp-a", "ru-central1-a", "a.example", domain.HypervisorCapacity{VCPUs: 32})
	require.NoError(t, err)
	require.Equal(t, uint32(0), a.NodeIndex)
	require.Equal(t, domain.HypervisorStateReady, a.State)
	b, err := svc.Register(ctx, "hyp-b", "ru-central1-b", "", domain.HypervisorCapacity{})
	require.NoError(t, err)
	require.Equal(t, uint32(1), b.NodeIndex)

	// idempotent on explicit id
	a2, err := svc.Register(ctx, "hyp-a", "ru-central1-a", "", domain.HypervisorCapacity{})
	require.NoError(t, err)
	require.Equal(t, a.NodeIndex, a2.NodeIndex)

	// generated id when empty
	c, err := svc.Register(ctx, "", "ru-central1-a", "", domain.HypervisorCapacity{})
	require.NoError(t, err)
	require.NotEmpty(t, c.ID)
	require.Equal(t, uint32(2), c.NodeIndex)

	// Get / List
	got, err := svc.Get(ctx, "hyp-a")
	require.NoError(t, err)
	require.Equal(t, "ru-central1-a", got.ZoneID)
	_, err = svc.Get(ctx, "no-such")
	require.Equal(t, codes.NotFound, status.Code(err))
	lst, _, err := svc.List(ctx, "", domain.HypervisorStateUnspecified, Pagination{})
	require.NoError(t, err)
	require.Len(t, lst, 3)
	lst, _, err = svc.List(ctx, "ru-central1-a", domain.HypervisorStateUnspecified, Pagination{})
	require.NoError(t, err)
	require.Len(t, lst, 2)

	// UpdateState
	up, err := svc.UpdateState(ctx, "hyp-a", domain.HypervisorStateCordoned, &domain.HypervisorCapacity{VCPUs: 64})
	require.NoError(t, err)
	require.Equal(t, domain.HypervisorStateCordoned, up.State)
	require.Equal(t, int64(64), up.Capacity.VCPUs)

	// Deregister -> node_index reused by the next register
	require.NoError(t, svc.Deregister(ctx, "hyp-a"))
	require.Equal(t, codes.NotFound, status.Code(svc.Deregister(ctx, "hyp-a")))
	d, err := svc.Register(ctx, "hyp-d", "ru-central1-a", "", domain.HypervisorCapacity{})
	require.NoError(t, err)
	require.Equal(t, uint32(0), d.NodeIndex, "освобождённый node_index переиспользован")
}
