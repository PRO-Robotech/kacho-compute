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
// NOT_FOUND) → Unavailable "zone check: ..." (fail-closed на мутации Instance;
// data-integrity.md §cross-domain — peer недоступен → Unavailable, не «зона ок»).
func TestMapZoneRefErr_GeoDown_Unavailable(t *testing.T) {
	err := mapZoneRefErr(status.Error(codes.Unavailable, "connection refused"), "ru-central1-a")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unavailable, st.Code())
	require.Contains(t, st.Message(), "zone check")
}
