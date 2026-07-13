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

// One-to-one NAT оперирует над адресацией конкретного network interface, которая
// редактируется через kacho-vpc NetworkInterface напрямую (NIC — first-class ресурс
// vpc), а не через Instance. Поэтому AddOneToOneNat / RemoveOneToOneNat / (и
// UpdateNetworkInterface) на InstanceService обязаны отдавать Unimplemented.
// NB: AttachNetworkInterface / DetachNetworkInterface — РЕАЛИЗОВАНЫ (S4, привязка
// существующего NIC к инстансу), это не Unimplemented-контракт.
func TestInstanceHandler_OneToOneNat_Unimplemented(t *testing.T) {
	h := &InstanceHandler{}

	_, err := h.AddOneToOneNat(context.Background(), &computev1.AddInstanceOneToOneNatRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err),
		"AddOneToOneNat must be Unimplemented (no auto-NIC — no interface to attach NAT to)")

	_, err = h.RemoveOneToOneNat(context.Background(), &computev1.RemoveInstanceOneToOneNatRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err),
		"RemoveOneToOneNat must be Unimplemented (no auto-NIC — no interface to detach NAT from)")
}
