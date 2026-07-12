// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package clients

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	storagev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/storage/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// fakeStorageClient — in-memory storagev1.InternalVolumeServiceClient for unit-testing
// the compute→storage adapter. Each closure fully controls one RPC's response.
type fakeStorageClient struct {
	attachFn func(ctx context.Context, in *storagev1.AttachVolumeRequest) (*storagev1.AttachVolumeResponse, error)
	detachFn func(ctx context.Context, in *storagev1.DetachVolumeRequest) (*storagev1.DetachVolumeResponse, error)
	listFn   func(ctx context.Context, in *storagev1.ListAttachmentsRequest) (*storagev1.ListAttachmentsResponse, error)
}

func (f *fakeStorageClient) Attach(ctx context.Context, in *storagev1.AttachVolumeRequest, _ ...grpc.CallOption) (*storagev1.AttachVolumeResponse, error) {
	return f.attachFn(ctx, in)
}

func (f *fakeStorageClient) Detach(ctx context.Context, in *storagev1.DetachVolumeRequest, _ ...grpc.CallOption) (*storagev1.DetachVolumeResponse, error) {
	return f.detachFn(ctx, in)
}

func (f *fakeStorageClient) ListAttachments(ctx context.Context, in *storagev1.ListAttachmentsRequest, _ ...grpc.CallOption) (*storagev1.ListAttachmentsResponse, error) {
	return f.listFn(ctx, in)
}

func (f *fakeStorageClient) GetInternal(_ context.Context, _ *storagev1.GetInternalVolumeRequest, _ ...grpc.CallOption) (*storagev1.VolumeInternal, error) {
	return nil, status.Error(codes.Unimplemented, "not used in test")
}

// TestStorageClient_Attach_ForwardsSpec_MirrorsResult — Attach forwards the full
// self-describing payload and distils the returned Volume's matching attachment into
// a VolumeAttachmentInfo (volume_id stamped from the Volume, coherent with the spec).
func TestStorageClient_Attach_ForwardsSpec_MirrorsResult(t *testing.T) {
	fake := &fakeStorageClient{attachFn: func(_ context.Context, in *storagev1.AttachVolumeRequest) (*storagev1.AttachVolumeResponse, error) {
		require.Equal(t, "vol-abc", in.GetVolumeId())
		require.Equal(t, "cim-xyz", in.GetInstanceId())
		require.Equal(t, "web-1", in.GetInstanceName())
		require.Equal(t, "ru-central1-a", in.GetInstanceZoneId())
		require.Equal(t, "prj-1", in.GetProjectId())
		require.Equal(t, "/dev/sdb", in.GetDeviceName())
		require.True(t, in.GetIsBoot())
		require.Equal(t, storagev1.VolumeAttachment_READ_ONLY, in.GetMode())
		require.True(t, in.GetAutoDelete())
		return &storagev1.AttachVolumeResponse{Volume: &storagev1.Volume{
			Id:     "vol-abc",
			ZoneId: "ru-central1-a",
			Status: storagev1.Volume_IN_USE,
			Attachments: []*storagev1.VolumeAttachment{{
				InstanceId:   "cim-xyz",
				InstanceName: "web-1",
				DeviceName:   "/dev/sdb",
				IsBoot:       true,
				Mode:         storagev1.VolumeAttachment_READ_ONLY,
				AutoDelete:   true,
			}},
		}}, nil
	}}
	c := NewStorageClientWith(fake)

	got, err := c.Attach(context.Background(), ports.VolumeAttachSpec{
		VolumeID:       "vol-abc",
		InstanceID:     "cim-xyz",
		InstanceName:   "web-1",
		InstanceZoneID: "ru-central1-a",
		ProjectID:      "prj-1",
		DeviceName:     "/dev/sdb",
		IsBoot:         true,
		Mode:           ports.VolumeAttachModeReadOnly,
		AutoDelete:     true,
	})
	require.NoError(t, err)
	require.Equal(t, ports.VolumeAttachmentInfo{
		VolumeID:     "vol-abc",
		InstanceID:   "cim-xyz",
		InstanceName: "web-1",
		DeviceName:   "/dev/sdb",
		IsBoot:       true,
		Mode:         ports.VolumeAttachModeReadOnly,
		AutoDelete:   true,
	}, *got)
}

// TestStorageClient_Attach_ContractErrorPreserved — a storage contract error
// (FailedPrecondition zone-mismatch) passes through with its code + message intact
// (it is the owner's own contract text, not a transport leak).
func TestStorageClient_Attach_ContractErrorPreserved(t *testing.T) {
	fake := &fakeStorageClient{attachFn: func(_ context.Context, _ *storagev1.AttachVolumeRequest) (*storagev1.AttachVolumeResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, "Instance is in zone ru-central1-b, Volume zone is ru-central1-a")
	}}
	c := NewStorageClientWith(fake)

	_, err := c.Attach(context.Background(), ports.VolumeAttachSpec{VolumeID: "vol-abc", InstanceID: "cim-xyz"})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "zone")
}

// TestStorageClient_Attach_Unavailable_FailClosed_LeakGuard — a transport-level
// Unavailable (carrying raw dial details) must fail-closed as codes.Unavailable with
// a FIXED opaque message: the caller-visible error must NOT leak the peer host/port
// or driver text (security.md hardening-invariant N1, applied to the peer-client).
func TestStorageClient_Attach_Unavailable_FailClosed_LeakGuard(t *testing.T) {
	const dialLeak = "connection refused: dial tcp 10.42.7.13:9091: i/o timeout"
	fake := &fakeStorageClient{attachFn: func(_ context.Context, _ *storagev1.AttachVolumeRequest) (*storagev1.AttachVolumeResponse, error) {
		return nil, status.Error(codes.Unavailable, dialLeak)
	}}
	c := NewStorageClientWith(fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // retry.OnUnavailable must break immediately, not wait out its budget

	_, err := c.Attach(ctx, ports.VolumeAttachSpec{VolumeID: "vol-abc", InstanceID: "cim-xyz"})
	require.Error(t, err, "storage-down must fail-closed, never silent success")
	require.Equal(t, codes.Unavailable, status.Code(err))
	msg := status.Convert(err).Message()
	require.NotContains(t, msg, "10.42.7.13", "dial host must not leak to the caller")
	require.NotContains(t, msg, "dial tcp", "transport text must not leak to the caller")
	require.False(t, strings.Contains(msg, "9091"), "dial port must not leak to the caller")
}

// TestStorageClient_Detach_Unavailable_FailClosed_LeakGuard — Detach mutation
// fail-closes with the same opaque Unavailable (no dial-detail leak).
func TestStorageClient_Detach_Unavailable_FailClosed_LeakGuard(t *testing.T) {
	const dialLeak = "rpc error: dial tcp 10.42.7.13:9091: connect: connection refused"
	fake := &fakeStorageClient{detachFn: func(_ context.Context, _ *storagev1.DetachVolumeRequest) (*storagev1.DetachVolumeResponse, error) {
		return nil, status.Error(codes.Unavailable, dialLeak)
	}}
	c := NewStorageClientWith(fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Detach(ctx, "vol-abc", "cim-xyz")
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "10.42.7.13")
	require.NotContains(t, status.Convert(err).Message(), "dial tcp")
}

// TestStorageClient_ListAttachments_Batched — ListAttachments forwards the instance
// batch and maps each VolumeAttachmentInfo wire record (with its volume_id) into the
// compute-side mirror value type.
func TestStorageClient_ListAttachments_Batched(t *testing.T) {
	fake := &fakeStorageClient{listFn: func(_ context.Context, in *storagev1.ListAttachmentsRequest) (*storagev1.ListAttachmentsResponse, error) {
		require.ElementsMatch(t, []string{"cim-1", "cim-2"}, in.GetInstanceIds())
		return &storagev1.ListAttachmentsResponse{Attachments: []*storagev1.VolumeAttachmentInfo{
			{VolumeId: "vol-1", InstanceId: "cim-1", DeviceName: "/dev/sda", IsBoot: true, Mode: storagev1.VolumeAttachment_READ_WRITE},
			{VolumeId: "vol-2", InstanceId: "cim-2", DeviceName: "/dev/sdb", AutoDelete: true, Mode: storagev1.VolumeAttachment_READ_ONLY},
		}}, nil
	}}
	c := NewStorageClientWith(fake)

	got, err := c.ListAttachments(context.Background(), []string{"cim-1", "cim-2"})
	require.NoError(t, err)
	require.Equal(t, []ports.VolumeAttachmentInfo{
		{VolumeID: "vol-1", InstanceID: "cim-1", DeviceName: "/dev/sda", IsBoot: true, Mode: ports.VolumeAttachModeReadWrite},
		{VolumeID: "vol-2", InstanceID: "cim-2", DeviceName: "/dev/sdb", AutoDelete: true, Mode: ports.VolumeAttachModeReadOnly},
	}, got)
}

// TestStorageClient_ListAttachments_BlockingPeer_TimesOut — an app-slow storage peer
// (alive, connected, never responds → NOT codes.Unavailable) must be bounded by the
// client's own per-call timeout, not hang for the life of the caller's ctx
// (architecture.md: per-call deadline on every external call).
func TestStorageClient_ListAttachments_BlockingPeer_TimesOut(t *testing.T) {
	unblock := make(chan struct{})
	defer close(unblock)
	fake := &fakeStorageClient{listFn: func(ctx context.Context, _ *storagev1.ListAttachmentsRequest) (*storagev1.ListAttachmentsResponse, error) {
		select {
		case <-ctx.Done():
			return nil, status.Error(codes.DeadlineExceeded, ctx.Err().Error())
		case <-unblock:
			t.Error("peer must never observe unblock — call should return via its own per-call timeout first")
			return nil, status.Error(codes.Unavailable, "unreachable")
		}
	}}
	c := NewStorageClientWith(fake)
	c.timeout = 20 * time.Millisecond

	start := time.Now()
	_, err := c.ListAttachments(context.Background(), []string{"cim-1"}) // caller ctx has NO deadline
	elapsed := time.Since(start)

	require.Error(t, err)
	require.Less(t, elapsed, 2*time.Second, "must return around its own per-call timeout, not hang")
}

// TestNoopStorageClient_FailClosed — unconfigured edge: Attach/Detach fail-closed
// Unavailable (attach requires a live kacho-storage), ListAttachments empty (mirror
// gracefully omitted).
func TestNoopStorageClient_FailClosed(t *testing.T) {
	var c ports.StorageClient = NoopStorageClient{}

	_, aerr := c.Attach(context.Background(), ports.VolumeAttachSpec{VolumeID: "vol-abc"})
	require.Equal(t, codes.Unavailable, status.Code(aerr))
	require.Error(t, c.Detach(context.Background(), "vol-abc", "cim-xyz"))
	require.Equal(t, codes.Unavailable, status.Code(c.Detach(context.Background(), "vol-abc", "cim-xyz")))

	att, lerr := c.ListAttachments(context.Background(), []string{"cim-1"})
	require.NoError(t, lerr)
	require.Empty(t, att)
}

// staticAssertStorageClientPort — StorageClient impls must satisfy the use-case port.
var (
	_ service.StorageClient = (*StorageClient)(nil)
	_ service.StorageClient = NoopStorageClient{}
)
