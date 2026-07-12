// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package config

import (
	"fmt"

	"google.golang.org/grpc"

	corecfg "github.com/PRO-Robotech/kacho-corelib/config"
	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
)

// envPrefix — корневой сегмент имён env для kacho-compute (KACHO_<DOMAIN>).
// LoadPrefixed выводит env-имя каждого поля из иерархии: envPrefix + tag/field.
const envPrefix = "KACHO_COMPUTE"

// Config — конфигурация kacho-compute.
type Config struct {
	DBHost     string `envconfig:"KACHO_COMPUTE_DB_HOST" default:"localhost"`
	DBPort     string `envconfig:"KACHO_COMPUTE_DB_PORT" default:"5432"`
	DBUser     string `envconfig:"KACHO_COMPUTE_DB_USER" default:"compute"`
	DBPassword string `envconfig:"KACHO_COMPUTE_DB_PASSWORD" required:"true"`
	DBName     string `envconfig:"KACHO_COMPUTE_DB_NAME" default:"kacho_compute"`
	// DBSSLMode — sslmode для DSN. По умолчанию `disable` для dev-стенда;
	// production обязан выставить `verify-full` (security P0).
	DBSSLMode string `envconfig:"KACHO_COMPUTE_DB_SSLMODE" default:"disable"`
	// DBMaxConns — лимит pgx pool (0 = pgx default max(4, NumCPU)).
	DBMaxConns int `envconfig:"KACHO_COMPUTE_DB_MAX_CONNS" default:"0"`

	GrpcPort string `envconfig:"KACHO_COMPUTE_GRPC_PORT" default:"9090"`

	// InternalGrpcPort — порт для cluster-internal RPC (InternalWatchService,
	// InternalDiskTypeService). НЕ выставляется через api-gateway external endpoint.
	InternalGrpcPort string `envconfig:"KACHO_COMPUTE_INTERNAL_PORT" default:"9091"`

	// WatchMaxStreams — максимум одновременных Watch streams (каждый держит
	// dedicated pgx.Conn под LISTEN).
	WatchMaxStreams int `envconfig:"KACHO_COMPUTE_WATCH_MAX_STREAMS" default:"32"`

	// MetricsAddr — адрес cluster-internal diagnostic HTTP-listener'а
	// (/metrics + /healthz + /readyz). Default ":9095" — отдельный internal-порт
	// (НЕ маршрутизируется на external endpoint; cluster-internal scrape +
	// kubelet-проба /readyz). Пустое значение явно отключает listener (back-compat).
	MetricsAddr string `envconfig:"KACHO_COMPUTE_METRICS_ADDR" default:":9095"`

	// IAMGRPCAddr — адрес kacho-iam (ProjectService.Get — project-existence-check).
	IAMGRPCAddr string `envconfig:"KACHO_COMPUTE_IAM_GRPC_ADDR" default:"kacho-iam.kacho.svc.cluster.local:9090"`

	// GeoGRPCAddr — адрес kacho-geo (geo.v1.ZoneService.Get, public :9090) для
	// валидации Instance/Disk.zone_id. Geography (Region/Zone) — leaf-сервис
	// kacho-geo; compute больше не валидирует zone_id по своей таблице `zones` и не
	// обслуживает Region/Zone.
	GeoGRPCAddr string `envconfig:"KACHO_COMPUTE_GEO_GRPC_ADDR" default:"kacho-geo.kacho.svc.cluster.local:9090"`

	// VPCInternalGRPCAddr — адрес kacho-vpc internal listener (:9091) для
	// InternalNetworkInterfaceService.Attach/Detach/ListByInstance (NIC↔Instance
	// attach-saga, S4). Internal-only (не external endpoint). Пустое значение
	// → NIC-ребро не сконфигурировано (NoopNicClient: attach fail-closed Unavailable,
	// зеркало опускается).
	VPCInternalGRPCAddr string `envconfig:"KACHO_COMPUTE_VPC_INTERNAL_GRPC_ADDR" default:"kacho-vpc.kacho.svc.cluster.local:9091"`

	// SkipPeerValidation — отключить cross-service existence-check (folder в
	// kacho-iam, zone_id в kacho-geo) → no-op. Для unit/newman/load-тестов без
	// поднятых peer-сервисов.
	SkipPeerValidation bool `envconfig:"KACHO_COMPUTE_SKIP_PEER_VALIDATION" default:"false"`

	// AuthMode — fail-closed гейт перед IAM merge: `dev` | `production` | `production-strict`.
	AuthMode string `envconfig:"KACHO_COMPUTE_AUTH_MODE" default:"dev"`

	// AuthZIAMGRPCAddr — gRPC адрес kacho-iam internal-port'а для Check.
	// Если пуст и AuthZBreakglass=false — interceptor НЕ
	// навешивается (graceful start без kacho-iam в dev). Обычно совпадает
	// с IAMGRPCAddr (тот же сервис), но porт другой: 9091 (internal) vs
	// 9090 (публичный ProjectService.Get).
	AuthZIAMGRPCAddr string `envconfig:"KACHO_COMPUTE_AUTHZ_IAM_GRPC_ADDR" default:""`
	// AuthZBreakglass — emergency-режим: пропускать все RPC без Check + WARN.
	// Dev / break-glass only.
	AuthZBreakglass bool `envconfig:"KACHO_COMPUTE_AUTHZ_BREAKGLASS" default:"false"`

	// AuthZTrustedForwarderSANs — allow-list cert-identity SAN'ов, которым разрешено
	// форвардить end-user principal в x-kacho-principal-* metadata (обычно
	// единственный — api-gateway SA, SAN spiffe://kacho.cloud/ns/<ns>/sa/kacho-api-gateway).
	// Принимает comma-separated список. Пусто (default) → любой mTLS-verified peer
	// доверен как форвардер (паритет с insecure dev back-compat и kacho-iam) — допустимо
	// ТОЛЬКО в dev: validateAuthMode() fail-closed отвергает пустой список в любом
	// production-режиме (requireTrustedForwarders). Задаётся
	// в production для defense-in-depth против confused-deputy: внутренний сервис со
	// своим валидным client-cert'ом не сможет выдать себя за пользователя. На обоих
	// листенерах principal trust-gated через grpcsrv.UnaryCertIdentityExtract +
	// UnaryTrustedPrincipalExtract(WithTrustedForwarders(...)) — без verified cert'а
	// (или вне allow-list) forwarded principal снимается.
	AuthZTrustedForwarderSANs []string `envconfig:"KACHO_COMPUTE_AUTHZ_TRUSTED_FORWARDER_SANS"`

	// ===== FGA-filtered List =====
	//
	// Все ListFilter* — production-edition: configurable, no hardcoded.
	// Reuses AuthZIAMGRPCAddr (+ per-edge IAMAuthzMTLS creds) as the iam-authorize
	// endpoint (kacho-iam internal :9091 — AuthorizeService.ListObjects).

	// ListFilterEnabled — master-switch. true → handler вызывает iam.ListObjects
	// и фильтрует List по allow-list id. false → no filter (handler bypass).
	ListFilterEnabled bool `envconfig:"KACHO_COMPUTE_LIST_FILTER_ENABLED" default:"true"`

	// ListFilterTimeoutMs — per-request deadline для iam.ListObjects.
	// Default 500ms — exceeds nothing under SLA (p95 100ms target).
	ListFilterTimeoutMs int `envconfig:"KACHO_COMPUTE_LIST_FILTER_TIMEOUT_MS" default:"500"`

	// ListFilterCacheTTLMs — TTL in-memory decision cache. Short (5s) so что
	// access-binding revoke виден ≤5s; lower → больше RTT к iam.
	ListFilterCacheTTLMs int `envconfig:"KACHO_COMPUTE_LIST_FILTER_CACHE_TTL_MS" default:"5000"`

	// ListFilterCacheMaxEntries — bound для cache. TTL-primary eviction; при
	// превышении bound сбрасывается одна произвольная запись (не LRU — см.
	// authzfilter.putCache).
	// 10000 enough для ~1000 concurrent users × 10 unique (subject, type, action) keys.
	ListFilterCacheMaxEntries int `envconfig:"KACHO_COMPUTE_LIST_FILTER_CACHE_MAX_ENTRIES" default:"10000"`

	// ListFilterFailOpen — degraded mode. true → на FGA error: handler возвращает
	// все ресурсы caller'у (без фильтра); false → Unavailable. **Default false**
	// (fail-closed = secure). Set to true только в break-glass.
	ListFilterFailOpen bool `envconfig:"KACHO_COMPUTE_LIST_FILTER_FAIL_OPEN" default:"false"`

	// ===== register-drainer (FGA owner-tuple через kacho-iam) =====
	//
	// FGARegisterDrainerEnabled — включает register-drainer (corelib outbox/drainer):
	// дренит compute_fga_register_outbox, применяя intent через
	// InternalIAMService.RegisterResource/UnregisterResource. Default-on
	// в dev (без него созданные ресурсы не получат owner-tuple → per-resource Check
	// DENY). Это in-process goroutine, не cross-cluster rollout-flag.
	FGARegisterDrainerEnabled bool `envconfig:"KACHO_COMPUTE_FGA_REGISTER_DRAINER_ENABLED" default:"true"`

	// RequireIAM — fail-closed boot-gate. When true,
	// mutating Create is refused (UNAVAILABLE) and readiness is NotReady until the
	// register-drainer is IAM-connected, so no resource is ever created without a
	// deliverable owner-tuple intent. Default false (dev back-compat: old Warn
	// behaviour, Create allowed). In production: true (single canonical mode, N5).
	RequireIAM bool `envconfig:"KACHO_COMPUTE_REQUIRE_IAM" default:"false"`

	// ===== opt-in mTLS (per-edge) =====
	//
	// Каждое ребро — независимый grpcclient.TLSClient / grpcsrv.TLSServer value-struct.
	// envconfig.Process(envPrefix, &cfg) выводит env-имена из тега родительского поля:
	// `IAM_REGISTER_MTLS` → KACHO_COMPUTE_IAM_REGISTER_MTLS_{ENABLE,CERTFILE,KEYFILE,
	// CAFILES,SERVERNAME}. Enable=false (default) → insecure (dev backward-compat).
	// Per-edge enable → независимый rollback/rollout.

	// IAMRegisterMTLS — client-creds для ребра compute→iam (register-drainer →
	// InternalIAMService.RegisterResource/UnregisterResource). FGA-proxy edge.
	IAMRegisterMTLS grpcclient.TLSClient `envconfig:"IAM_REGISTER_MTLS"`

	// ===== CLIENT mTLS на read/authz рёбрах compute→iam =====
	//
	// register-drainer ребро закрыто отдельно (IAMRegisterMTLS). Это — зеркало того
	// же паттерна на ОСТАВШИХСЯ read/authz iam-conn'ах, которые ранее диалились
	// server-auth-only bool-флагами БЕЗ client-cert (флаги удалены). Когда iam
	// требует RequireAndVerifyClientCert на обоих listener'ах, такой dial падает на
	// TLS-handshake — оба ребра ОБЯЗАНЫ предъявлять kacho-compute-client-tls cert
	// (completeness-инвариант). Два отдельных поля, т.к. ServerName различается
	// per-listener: ProjectService.Get → :9090 (kacho-iam), Check/list-filter →
	// :9091 (kacho-iam-internal); один общий TLSClient не несёт оба ServerName.

	// IAMProjectMTLS — client-creds для ребра compute→iam ProjectService.Get
	// (existence + leaf-owner, public :9090). ServerName = kacho-iam.*.
	IAMProjectMTLS grpcclient.TLSClient `envconfig:"IAM_PROJECT_MTLS"`

	// IAMAuthzMTLS — client-creds для ребра compute→iam per-RPC
	// InternalIAMService.Check + FGA-filtered List (один conn → AuthZIAMGRPCAddr,
	// internal :9091). ServerName = kacho-iam-internal.*.
	IAMAuthzMTLS grpcclient.TLSClient `envconfig:"IAM_AUTHZ_MTLS"`

	// GeoMTLS — client-creds для ребра compute→geo (geo.v1.ZoneService.Get,
	// zone_id-валидация Instance). Enable=false (default) → insecure
	// (dev backward-compat); enable=true без валидного cert-trio → startup error
	// (fail-closed, без silent insecure-fallback) — паритет с IAM*MTLS.
	GeoMTLS grpcclient.TLSClient `envconfig:"GEO_MTLS"`

	// VPCNicMTLS — client-creds для ребра compute→vpc InternalNetworkInterfaceService
	// (:9091 internal). Enable=false (default) → insecure (dev backward-compat);
	// enable=true без валидного cert-trio → startup error (fail-closed) — паритет с
	// Geo/IAM*MTLS.
	VPCNicMTLS grpcclient.TLSClient `envconfig:"VPC_NIC_MTLS"`

	// PublicServerMTLS — server-creds для публичного listener (:9090, GrpcPort).
	PublicServerMTLS grpcsrv.TLSServer `envconfig:"PUBLIC_SERVER_MTLS"`

	// InternalServerMTLS — server-creds для cluster-internal listener (:9091,
	// InternalGrpcPort).
	InternalServerMTLS grpcsrv.TLSServer `envconfig:"INTERNAL_SERVER_MTLS"`
}

// IAMRegisterClientCreds возвращает grpc.DialOption для ребра compute→iam
// (register-drainer). Enable=false → insecure (dev backward-compat); enable=true
// без валидного cert-trio → error (fail-closed, без silent insecure-fallback).
func (c Config) IAMRegisterClientCreds() (grpc.DialOption, error) {
	return grpcclient.TLSClientCreds(c.IAMRegisterMTLS)
}

// IAMProjectClientCreds возвращает grpc.DialOption для ребра compute→iam
// ProjectService.Get (existence/leaf-owner, :9090). Enable=false → insecure (dev);
// enable=true без валидного cert-trio → error (fail-closed).
func (c Config) IAMProjectClientCreds() (grpc.DialOption, error) {
	return grpcclient.TLSClientCreds(c.IAMProjectMTLS)
}

// IAMAuthzClientCreds возвращает grpc.DialOption для ребра compute→iam
// InternalIAMService.Check + FGA-filtered List (:9091). Enable=false → insecure
// (dev); enable=true без валидного cert-trio → error (fail-closed).
func (c Config) IAMAuthzClientCreds() (grpc.DialOption, error) {
	return grpcclient.TLSClientCreds(c.IAMAuthzMTLS)
}

// GeoClientCreds возвращает grpc.DialOption для ребра compute→geo
// (geo.v1.ZoneService.Get, zone_id-валидация Instance, S4). Enable=false →
// insecure (dev); enable=true без валидного cert-trio → error (fail-closed).
func (c Config) GeoClientCreds() (grpc.DialOption, error) {
	return grpcclient.TLSClientCreds(c.GeoMTLS)
}

// VPCNicClientCreds возвращает grpc.DialOption для ребра compute→vpc
// InternalNetworkInterfaceService (NIC-attach saga, :9091 internal). Enable=false →
// insecure (dev); enable=true без валидного cert-trio → error (fail-closed).
func (c Config) VPCNicClientCreds() (grpc.DialOption, error) {
	return grpcclient.TLSClientCreds(c.VPCNicMTLS)
}

// PublicServerCreds возвращает grpc.ServerOption для публичного listener (:9090).
func (c Config) PublicServerCreds() (grpc.ServerOption, error) {
	return grpcsrv.TLSServerCreds(c.PublicServerMTLS)
}

// InternalServerCreds возвращает grpc.ServerOption для internal listener (:9091).
func (c Config) InternalServerCreds() (grpc.ServerOption, error) {
	return grpcsrv.TLSServerCreds(c.InternalServerMTLS)
}

// baseDSN — стандартный postgres DSN без pgxpool-специфичных параметров
// (пригоден и для pgxpool, и для database/sql.Open("pgx")).
func (c Config) baseDSN() string {
	mode := c.DBSSLMode
	if mode == "" {
		mode = "disable"
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=%s",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName, mode,
	)
}

// DSN — connection string для pgxpool (поддерживает pool_max_conns).
// НЕ использовать для database/sql.Open("pgx") (pool_max_conns → unknown PG-param → FATAL).
func (c Config) DSN() string {
	dsn := c.baseDSN()
	if c.DBMaxConns > 0 {
		dsn += fmt.Sprintf("&pool_max_conns=%d", c.DBMaxConns)
	}
	return dsn
}

// MigrateDSN — connection string для goose/database/sql и dedicated Watch-conn
// (без pgxpool-параметров).
func (c Config) MigrateDSN() string {
	return c.baseDSN()
}

// Load загружает конфигурацию из переменных окружения.
//
// Использует LoadPrefixed(envPrefix): абсолютно-тегированные поля
// (`envconfig:"KACHO_COMPUTE_..."`) резолвятся как есть, а вложенные
// per-edge TLS value-структуры (grpcclient.TLSClient / grpcsrv.TLSServer) с
// относительным тегом (`IAM_REGISTER_MTLS`) получают независимые
// KACHO_COMPUTE_<EDGE>_<NAME> имена (per-edge prefixing).
func Load() (Config, error) {
	var c Config
	err := corecfg.LoadPrefixed(envPrefix, &c)
	return c, err
}
