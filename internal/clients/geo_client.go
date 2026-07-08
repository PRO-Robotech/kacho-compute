// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package clients

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	geov1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// defaultZoneCallTimeout — per-call deadline applied to EVERY geo.v1.ZoneService.Get
// attempt (audit-r6 P1). retry.OnUnavailable only classifies codes.Unavailable —
// an app-slow (not down) geo peer that never responds has no bound of its own and
// would hang the caller for the lifetime of the inbound ctx (LRO worker opTimeout,
// ~4min), exhausting worker slots under load. No configurable knob exists for this
// edge yet (unlike authzfilter.Config.Timeout) — package default const, per
// architecture.md's documented fallback.
const defaultZoneCallTimeout = 5 * time.Second

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
	// timeout — per-call deadline applied to every Get attempt (defaultZoneCallTimeout
	// unless overridden, e.g. by a test seam via direct field assignment).
	timeout time.Duration
}

// NewGeoClient создаёт GeoClient поверх gRPC-conn к kacho-geo (:9090,
// public ZoneService.Get).
func NewGeoClient(conn *grpc.ClientConn) *GeoClient {
	return &GeoClient{zones: geov1.NewZoneServiceClient(conn), timeout: defaultZoneCallTimeout}
}

// NewGeoClientWith создаёт GeoClient поверх готового geov1.ZoneServiceClient
// (seam для unit-тестов с fake-клиентом).
func NewGeoClientWith(zones geov1.ZoneServiceClient) *GeoClient {
	return &GeoClient{zones: zones, timeout: defaultZoneCallTimeout}
}

// GetZone валидирует существование zone_id через geo.v1.ZoneService.Get.
// Найдено → nil. geo NOT_FOUND → ports.ErrNotFound (mapZoneRefErr → InvalidArgument).
// geo недоступен (после retry) → проброс gRPC Unavailable (mapZoneRefErr → Unavailable).
//
// Каждая попытка (в т.ч. каждый retry.OnUnavailable-повтор) несёт собственный
// context.WithTimeout(c.timeout) — app-slow geo (peer жив, но не отвечает) бьётся
// per-call deadline'ом, а не висит до inbound ctx (worker opTimeout), см. audit-r6 P1.
func (c *GeoClient) GetZone(ctx context.Context, zoneID string) error {
	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		_, rerr := c.zones.Get(auth.PropagateOutgoing(callCtx), &geov1.GetZoneRequest{ZoneId: zoneID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				return ports.ErrNotFound
			}
			return rerr
		}
		return nil
	})
}

// NoopGeoClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true (zone
// existence-check отключён → любая зона «существует») и для unit/newman без
// поднятого kacho-geo. GetZone всегда успешен (любая зона существует).
type NoopGeoClient struct{}

// GetZone всегда возвращает nil без обращения к geo.
func (NoopGeoClient) GetZone(_ context.Context, _ string) error {
	return nil
}

// ensure compile-time: both impls satisfy the use-case port.
var (
	_ ports.ZoneRegistry = (*GeoClient)(nil)
	_ ports.ZoneRegistry = NoopGeoClient{}
)
