// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package clients

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	geov1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// GeoClient реализует ports.ZoneRegistry через gRPC к kacho-geo
// (geo.v1.ZoneService.Get) — источник existence-check для Instance.zone_id.
// Geography (Region/Zone) — домен leaf-сервиса kacho-geo; compute зонами не
// владеет и их не обслуживает, лишь валидирует свой zone_id как consumer.
//
// Контракт ZoneRegistry (см. mapZoneRefErr): geo NOT_FOUND → ports.ErrNotFound
// (→ InvalidArgument "Zone <id> not found", fail-closed на мутации Instance);
// транспортная ошибка (geo недоступен) → проброс gRPC Unavailable (→ Unavailable
// "zone check: ...", fail-closed для мутаций).
//
// W1.4-паритет с iam/vpc-клиентами: outgoing ctx обёрнут auth.PropagateOutgoing,
// чтобы geo-side authz-Check (security.md: per-RPC Check на каждом RPC) видел
// реального caller'а; retry.OnUnavailable сглаживает транзиентные обрывы.
type GeoClient struct {
	zones geov1.ZoneServiceClient
}

// NewGeoClient создаёт GeoClient поверх gRPC-conn к kacho-geo (:9090,
// public ZoneService.Get).
func NewGeoClient(conn *grpc.ClientConn) *GeoClient {
	return &GeoClient{zones: geov1.NewZoneServiceClient(conn)}
}

// NewGeoClientWith создаёт GeoClient поверх готового geov1.ZoneServiceClient
// (seam для unit-тестов с fake-клиентом).
func NewGeoClientWith(zones geov1.ZoneServiceClient) *GeoClient {
	return &GeoClient{zones: zones}
}

// GetZone валидирует zone_id через geo.v1.ZoneService.Get. Найдено → ZoneInfo
// (id + region). geo NOT_FOUND → ports.ErrNotFound (mapZoneRefErr → InvalidArgument).
// geo недоступен (после retry) → проброс gRPC Unavailable (mapZoneRefErr → Unavailable).
func (c *GeoClient) GetZone(ctx context.Context, zoneID string) (ports.ZoneInfo, error) {
	var out ports.ZoneInfo
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		z, rerr := c.zones.Get(auth.PropagateOutgoing(ctx), &geov1.GetZoneRequest{ZoneId: zoneID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				return ports.ErrNotFound
			}
			return rerr
		}
		out = ports.ZoneInfo{ID: z.GetId(), RegionID: z.GetRegionId()}
		return nil
	})
	if err != nil {
		return ports.ZoneInfo{}, err
	}
	return out, nil
}

// NoopGeoClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true (zone
// existence-check отключён → любая зона «существует») и для unit/newman без
// поднятого kacho-geo. GetZone возвращает ZoneInfo с переданным id (region пуст).
type NoopGeoClient struct{}

// GetZone всегда возвращает ZoneInfo{ID: zoneID} без обращения к geo.
func (NoopGeoClient) GetZone(_ context.Context, zoneID string) (ports.ZoneInfo, error) {
	return ports.ZoneInfo{ID: zoneID}, nil
}

// ensure compile-time: both impls satisfy the use-case port.
var (
	_ ports.ZoneRegistry = (*GeoClient)(nil)
	_ ports.ZoneRegistry = NoopGeoClient{}
)
