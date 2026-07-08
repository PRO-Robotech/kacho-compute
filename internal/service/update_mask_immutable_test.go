// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestUpdateMask_ImmutableFieldMessage locks the api-conventions rule: an
// update_mask carrying a hard-immutable field must fail with
// InvalidArgument "<field> is immutable after <R>.Create" — NOT the generic
// corevalidate.UpdateMask "unknown field in update_mask" message. Regression
// guard: the immutable-field check must run BEFORE the known-set UpdateMask
// check (which does not list immutable fields, so it would otherwise shadow the
// convention message and leave the immutable branch unreachable).
func TestUpdateMask_ImmutableFieldMessage(t *testing.T) {
	cases := []struct {
		name  string
		field string
		res   string
		err   error
	}{
		{"instance_zone_id", "zone_id", "Instance", validateInstanceUpdate(UpdateInstanceReq{UpdateMask: []string{"zone_id"}})},
		{"instance_boot_disk", "boot_disk", "Instance", validateInstanceUpdate(UpdateInstanceReq{UpdateMask: []string{"boot_disk"}})},
		{"instance_metadata", "metadata", "Instance", validateInstanceUpdate(UpdateInstanceReq{UpdateMask: []string{"metadata"}})},
		{"disk_type_id", "type_id", "Disk", validateDiskUpdate(UpdateDiskReq{UpdateMask: []string{"type_id"}})},
		{"disk_block_size", "block_size", "Disk", validateDiskUpdate(UpdateDiskReq{UpdateMask: []string{"block_size"}})},
		{"image_family", "family", "Image", validateImageUpdate(UpdateImageReq{UpdateMask: []string{"family"}})},
		{"image_pooled", "pooled", "Image", validateImageUpdate(UpdateImageReq{UpdateMask: []string{"pooled"}})},
		{"snapshot_source_disk_id", "source_disk_id", "Snapshot", validateSnapshotUpdate(UpdateSnapshotReq{UpdateMask: []string{"source_disk_id"}})},
		{"snapshot_disk_size", "disk_size", "Snapshot", validateSnapshotUpdate(UpdateSnapshotReq{UpdateMask: []string{"disk_size"}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, tc.err)
			st, ok := status.FromError(tc.err)
			require.True(t, ok, "expected gRPC status error")
			require.Equal(t, codes.InvalidArgument, st.Code())
			want := tc.field + " is immutable after " + tc.res + ".Create"
			require.Truef(t, strings.HasPrefix(st.Message(), want),
				"got %q, want prefix %q (convention message, not generic unknown-field)", st.Message(), want)
			require.NotContains(t, st.Message(), "unknown field in update_mask",
				"immutable field must not surface the generic UpdateMask known-set error")
		})
	}
}
