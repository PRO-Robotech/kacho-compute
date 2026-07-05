// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMapZoneRefErr_NotFound_InvalidArgument — geo вернул NOT_FOUND (через
// ZoneRegistry.ErrNotFound) → InvalidArgument "Zone <id> not found" (контракт
// compute сохранён при переключении zone-валидации с self на kacho-geo, S4).
func TestMapZoneRefErr_NotFound_InvalidArgument(t *testing.T) {
	err := mapZoneRefErr(ErrNotFound, "no-such-zone")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "no-such-zone")
}

// TestMapZoneRefErr_GeoNotFoundStatus_InvalidArgument — geo-клиент пробросил
// gRPC NOT_FOUND как status (не sentinel) → тоже InvalidArgument.
func TestMapZoneRefErr_GeoNotFoundStatus_InvalidArgument(t *testing.T) {
	err := mapZoneRefErr(status.Error(codes.NotFound, "Zone x not found"), "ru-central1-z")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Contains(t, st.Message(), "ru-central1-z")
}

// TestMapZoneRefErr_GeoDown_Unavailable — geo недоступен (transport-ошибка, не
// NOT_FOUND) → Unavailable "zone check: ..." (fail-closed на мутации Instance:
// peer недоступен → Unavailable, не «зона ок»).
func TestMapZoneRefErr_GeoDown_Unavailable(t *testing.T) {
	err := mapZoneRefErr(status.Error(codes.Unavailable, "connection refused to 10.4.2.7:9091"), "ru-central1-a")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unavailable, st.Code())
	require.Contains(t, st.Message(), "zone check")
	// Raw peer transport detail (endpoint / dial error) must NOT be echoed to the
	// tenant — opaque message only (CWE-209), mirroring mapRepoErr discipline.
	require.NotContains(t, st.Message(), "connection refused")
	require.NotContains(t, st.Message(), "10.4.2.7")
}
