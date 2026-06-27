package clients

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	geov1 "github.com/PRO-Robotech/kacho-geo/proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// fakeGeoZoneClient — in-memory geov1.ZoneServiceClient для unit-теста geo-client'а.
// getFn полностью контролирует ответ Get (found / not-found / down).
type fakeGeoZoneClient struct {
	getFn func(ctx context.Context, in *geov1.GetZoneRequest) (*geov1.Zone, error)
}

func (f *fakeGeoZoneClient) Get(ctx context.Context, in *geov1.GetZoneRequest, _ ...grpc.CallOption) (*geov1.Zone, error) {
	return f.getFn(ctx, in)
}

func (f *fakeGeoZoneClient) List(_ context.Context, _ *geov1.ListZonesRequest, _ ...grpc.CallOption) (*geov1.ListZonesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used in test")
}

// TestGeoClient_GetZone_Found — geo возвращает зону → GeoClient отдаёт ZoneInfo
// с id+region, nil error.
func TestGeoClient_GetZone_Found(t *testing.T) {
	fake := &fakeGeoZoneClient{getFn: func(_ context.Context, in *geov1.GetZoneRequest) (*geov1.Zone, error) {
		require.Equal(t, "ru-central1-a", in.GetZoneId())
		return &geov1.Zone{Id: "ru-central1-a", RegionId: "ru-central1", Status: geov1.Zone_UP}, nil
	}}
	c := NewGeoClientWith(fake)

	zi, err := c.GetZone(context.Background(), "ru-central1-a")
	require.NoError(t, err)
	require.Equal(t, service.ZoneInfo{ID: "ru-central1-a", RegionID: "ru-central1"}, zi)
}

// TestGeoClient_GetZone_NotFound — geo отвечает NOT_FOUND → GeoClient возвращает
// service.ErrNotFound (его ловит mapZoneRefErr → InvalidArgument "Zone ... not found",
// fail-closed на мутации Instance).
func TestGeoClient_GetZone_NotFound(t *testing.T) {
	fake := &fakeGeoZoneClient{getFn: func(_ context.Context, _ *geov1.GetZoneRequest) (*geov1.Zone, error) {
		return nil, status.Error(codes.NotFound, "Zone no-such-zone not found")
	}}
	c := NewGeoClientWith(fake)

	_, err := c.GetZone(context.Background(), "no-such-zone")
	require.Error(t, err)
	require.True(t, errors.Is(err, ports.ErrNotFound), "geo NOT_FOUND must map to service.ErrNotFound, got %v", err)
}

// TestGeoClient_GetZone_Unavailable — geo недоступен (transport-ошибка) →
// GeoClient НЕ трактует это как not-found и НЕ возвращает nil (fail-closed для
// мутаций Instance: mapZoneRefErr пробросит non-NotFound как Unavailable
// "zone check: ...", см. data-integrity.md §cross-domain).
//
// Retry.OnUnavailable имеет 30s-бюджет; чтобы тест был быстрым и детерминированным,
// контекст отменяется до вызова — retry-цикл прерывается немедленно после первой
// неуспешной попытки (см. retry.OnCodes: select <-ctx.Done()). Главный инвариант,
// который проверяем: транзиентная ошибка peer'а НИКОГДА не классифицируется как
// ErrNotFound и НИКОГДА не «успех».
func TestGeoClient_GetZone_Unavailable(t *testing.T) {
	fake := &fakeGeoZoneClient{getFn: func(_ context.Context, _ *geov1.GetZoneRequest) (*geov1.Zone, error) {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}}
	c := NewGeoClientWith(fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // retry-цикл должен прерваться немедленно, не ждать 30s

	zi, err := c.GetZone(ctx, "ru-central1-a")
	require.Error(t, err, "geo-down must propagate an error, never silent success")
	require.Equal(t, service.ZoneInfo{}, zi, "no zone info on geo-down")
	require.False(t, errors.Is(err, ports.ErrNotFound), "transport error must NOT be treated as not-found (fail-closed)")
}

// staticAssertGeoClientPort — GeoClient должен реализовывать service.ZoneRegistry
// (тот же порт, что и in-process ZoneRepoSource), чтобы заменить его в wiring.
var _ service.ZoneRegistry = (*GeoClient)(nil)
