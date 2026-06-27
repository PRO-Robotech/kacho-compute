package clients

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	geov1 "github.com/PRO-Robotech/kacho-geo/proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// GeoClient реализует service.ZoneRegistry через gRPC к kacho-geo
// (geo.v1.ZoneService.Get). Заменяет in-process ZoneRepoSource как источник
// existence-check для Instance.zone_id (эпик kacho-geo, Stage S4): Geography —
// домен нового leaf-сервиса kacho-geo, compute больше не владеет зонами для
// валидации (compute по-прежнему СЛУЖИТ свой Region/Zone до S7 — снимается там).
//
// Контракт ZoneRegistry (см. mapZoneRefErr): geo NOT_FOUND → service.ErrNotFound
// (→ InvalidArgument "Zone <id> not found", fail-closed на мутации Instance);
// транспортная ошибка (geo недоступен) → проброс gRPC Unavailable (→ Unavailable
// "zone check: ...", fail-closed для мутаций, data-integrity.md §cross-domain).
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
// (id + region). geo NOT_FOUND → service.ErrNotFound (mapZoneRefErr → InvalidArgument).
// geo недоступен (после retry) → проброс gRPC Unavailable (mapZoneRefErr → Unavailable).
func (c *GeoClient) GetZone(ctx context.Context, zoneID string) (service.ZoneInfo, error) {
	var out service.ZoneInfo
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		z, rerr := c.zones.Get(auth.PropagateOutgoing(ctx), &geov1.GetZoneRequest{ZoneId: zoneID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				return ports.ErrNotFound
			}
			return rerr
		}
		out = service.ZoneInfo{ID: z.GetId(), RegionID: z.GetRegionId()}
		return nil
	})
	if err != nil {
		return service.ZoneInfo{}, err
	}
	return out, nil
}

// NoopGeoClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true (zone
// existence-check отключён → любая зона «существует») и для unit/newman без
// поднятого kacho-geo. GetZone возвращает ZoneInfo с переданным id (region пуст).
type NoopGeoClient struct{}

// GetZone всегда возвращает ZoneInfo{ID: zoneID} без обращения к geo.
func (NoopGeoClient) GetZone(_ context.Context, zoneID string) (service.ZoneInfo, error) {
	return service.ZoneInfo{ID: zoneID}, nil
}

// ensure compile-time: both impls satisfy the use-case port.
var (
	_ service.ZoneRegistry = (*GeoClient)(nil)
	_ service.ZoneRegistry = NoopGeoClient{}
)
