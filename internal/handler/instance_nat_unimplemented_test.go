// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

// One-to-one NAT оперирует над network interface, а Instance создаётся без NIC
// (no auto-NIC): NIC-привязка вынесена из lifecycle Instance целиком. Поэтому
// AddOneToOneNat / RemoveOneToOneNat не могут выполнить свою работу и обязаны
// отдавать Unimplemented — тот же контракт, что и остальные NIC-RPC
// (AttachNetworkInterface / DetachNetworkInterface / UpdateNetworkInterface).
func TestInstanceHandler_OneToOneNat_Unimplemented(t *testing.T) {
	h := &InstanceHandler{}

	_, err := h.AddOneToOneNat(context.Background(), &computev1.AddInstanceOneToOneNatRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err),
		"AddOneToOneNat must be Unimplemented (no auto-NIC — no interface to attach NAT to)")

	_, err = h.RemoveOneToOneNat(context.Background(), &computev1.RemoveInstanceOneToOneNatRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err),
		"RemoveOneToOneNat must be Unimplemented (no auto-NIC — no interface to detach NAT from)")
}
