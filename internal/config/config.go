package config

import (
	"fmt"
	"os"

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
	// InternalDiskTypeService, InternalZoneService). НЕ выставляется через
	// api-gateway external endpoint.
	InternalGrpcPort string `envconfig:"KACHO_COMPUTE_INTERNAL_PORT" default:"9091"`

	// WatchMaxStreams — максимум одновременных Watch streams (каждый держит
	// dedicated pgx.Conn под LISTEN).
	WatchMaxStreams int `envconfig:"KACHO_COMPUTE_WATCH_MAX_STREAMS" default:"32"`

	// IAMGRPCAddr — адрес kacho-iam (ProjectService.Get; KAC-106 E1: переключение
	// с kacho-resource-manager на kacho-iam). KAC-127: legacy RM-fallback удалён.
	IAMGRPCAddr string `envconfig:"KACHO_COMPUTE_IAM_GRPC_ADDR" default:"kacho-iam.kacho.svc.cluster.local:9090"`
	// IAMTLS — TLS для cross-service gRPC к kacho-iam.
	IAMTLS bool `envconfig:"KACHO_COMPUTE_IAM_TLS" default:"false"`

	// VPCGRPCAddr — адрес kacho-vpc (SubnetService/SecurityGroupService/AddressService.Get
	// для валидации Instance network_interface_spec).
	VPCGRPCAddr string `envconfig:"KACHO_COMPUTE_VPC_GRPC_ADDR" default:"vpc.kacho.svc.cluster.local:9090"`
	// VPCTLS — TLS для cross-service gRPC к vpc.
	VPCTLS bool `envconfig:"KACHO_COMPUTE_VPC_TLS" default:"false"`

	// VPCInternalGRPCAddr — адрес internal-порта kacho-vpc (порт 9091:
	// InternalZoneService — compute берёт справочник зон из VPC-модуля, локальная
	// таблица `zones` используется только как fallback при SKIP_PEER_VALIDATION).
	VPCInternalGRPCAddr string `envconfig:"KACHO_COMPUTE_VPC_INTERNAL_GRPC_ADDR" default:"vpc.kacho.svc.cluster.local:9091"`
	// VPCInternalTLS — TLS для cross-service gRPC к internal-порту vpc.
	VPCInternalTLS bool `envconfig:"KACHO_COMPUTE_VPC_INTERNAL_TLS" default:"false"`

	// SkipPeerValidation — отключить cross-service existence-check (subnet/SG/address
	// в VPC, folder в RM) → no-op. Для unit/newman/load-тестов без поднятых peer-сервисов.
	SkipPeerValidation bool `envconfig:"KACHO_COMPUTE_SKIP_PEER_VALIDATION" default:"false"`

	// AuthMode — fail-closed гейт перед IAM merge: `dev` | `production` | `production-strict`.
	AuthMode string `envconfig:"KACHO_COMPUTE_AUTH_MODE" default:"dev"`

	// AuthZIAMGRPCAddr — gRPC адрес kacho-iam internal-port'а для Check
	// (E3 / KAC-108). Если пуст и AuthZBreakglass=false — interceptor НЕ
	// навешивается (graceful start без kacho-iam в dev). Обычно совпадает
	// с IAMGRPCAddr (тот же сервис), но porт другой: 9091 (internal) vs
	// 9090 (публичный ProjectService.Get).
	AuthZIAMGRPCAddr string `envconfig:"KACHO_COMPUTE_AUTHZ_IAM_GRPC_ADDR" default:""`
	// AuthZIAMTLS — TLS для AuthZ-вызовов к kacho-iam.
	AuthZIAMTLS bool `envconfig:"KACHO_COMPUTE_AUTHZ_IAM_TLS" default:"false"`
	// AuthZBreakglass — emergency-режим: пропускать все RPC без Check + WARN.
	// Dev / break-glass only (см. acceptance §6 D-6).
	AuthZBreakglass bool `envconfig:"KACHO_COMPUTE_AUTHZ_BREAKGLASS" default:"false"`

	// ===== KAC-127 Phase 4: FGA-filtered List =====
	//
	// Все ListFilter* — production-edition: configurable, no hardcoded.
	// Reuses AuthZIAMGRPCAddr/AuthZIAMTLS as the iam-authorize endpoint
	// (kacho-iam internal :9091 — AuthorizeService.ListObjects).

	// ListFilterEnabled — master-switch. true → handler вызывает iam.ListObjects
	// и фильтрует List по allow-list id. false → no filter (handler bypass).
	ListFilterEnabled bool `envconfig:"KACHO_COMPUTE_LIST_FILTER_ENABLED" default:"true"`

	// ListFilterTimeoutMs — per-request deadline для iam.ListObjects.
	// Default 500ms — exceeds nothing under SLA (p95 100ms target).
	ListFilterTimeoutMs int `envconfig:"KACHO_COMPUTE_LIST_FILTER_TIMEOUT_MS" default:"500"`

	// ListFilterCacheTTLMs — TTL in-memory decision cache. Short (5s) so что
	// access-binding revoke виден ≤5s; lower → больше RTT к iam.
	ListFilterCacheTTLMs int `envconfig:"KACHO_COMPUTE_LIST_FILTER_CACHE_TTL_MS" default:"5000"`

	// ListFilterCacheMaxEntries — bound для cache (LRU evict при превышении).
	// 10000 enough для ~1000 concurrent users × 10 unique (subject, type, action) keys.
	ListFilterCacheMaxEntries int `envconfig:"KACHO_COMPUTE_LIST_FILTER_CACHE_MAX_ENTRIES" default:"10000"`

	// ListFilterFailOpen — degraded mode. true → на FGA error: handler возвращает
	// все ресурсы caller'у (без фильтра); false → Unavailable. **Default false**
	// (fail-closed = secure). Set to true только в break-glass.
	ListFilterFailOpen bool `envconfig:"KACHO_COMPUTE_LIST_FILTER_FAIL_OPEN" default:"false"`

	// ===== SEC-D: register-drainer (FGA owner-tuple через kacho-iam) =====
	//
	// FGARegisterDrainerEnabled — включает register-drainer (corelib outbox/drainer):
	// дренит compute_fga_register_outbox, применяя intent через
	// InternalIAMService.RegisterResource/UnregisterResource. SEC-D OQ-5: default-on
	// в dev (без него созданные ресурсы не получат owner-tuple → per-resource Check
	// DENY). Это in-process goroutine, не cross-cluster rollout-flag.
	FGARegisterDrainerEnabled bool `envconfig:"KACHO_COMPUTE_FGA_REGISTER_DRAINER_ENABLED" default:"true"`

	// ===== SEC-D: opt-in mTLS (per-edge, corelib SEC-B) =====
	//
	// Каждое ребро — независимый grpcclient.TLSClient / grpcsrv.TLSServer value-struct.
	// envconfig.Process(envPrefix, &cfg) выводит env-имена из тега родительского поля:
	// `IAM_REGISTER_MTLS` → KACHO_COMPUTE_IAM_REGISTER_MTLS_{ENABLE,CERTFILE,KEYFILE,
	// CAFILES,SERVERNAME}. Enable=false (default) → insecure (dev backward-compat,
	// эпик §5). Per-edge enable → независимый rollback/rollout (§6.5).

	// IAMRegisterMTLS — client-creds для ребра compute→iam (register-drainer →
	// InternalIAMService.RegisterResource/UnregisterResource). FGA-proxy edge (SEC-A).
	IAMRegisterMTLS grpcclient.TLSClient `envconfig:"IAM_REGISTER_MTLS"`

	// VPCMTLS — client-creds для ребра compute→vpc (NIC-spec валидация + IPAM Address).
	VPCMTLS grpcclient.TLSClient `envconfig:"VPC_MTLS"`

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

// VPCClientCreds возвращает grpc.DialOption для ребра compute→vpc (NIC/IPAM).
func (c Config) VPCClientCreds() (grpc.DialOption, error) {
	return grpcclient.TLSClientCreds(c.VPCMTLS)
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
// KACHO_COMPUTE_<EDGE>_<NAME> имена (SEC-D, FD-3 per-edge prefixing).
func Load() (Config, error) {
	var c Config
	err := corecfg.LoadPrefixed(envPrefix, &c)
	return c, err
}

// LoadInto — тест-хелпер: выставляет переданные env-переменные на время вызова
// и загружает конфиг через тот же LoadPrefixed-путь, что и Load (без глобального
// state-leak между тестами — все ключи восстанавливаются через t.Setenv-семантику
// os.Setenv/Unsetenv с очисткой). Используется mTLS-конфиг-тестами (SEC-D).
func LoadInto(c *Config, env map[string]string) error {
	saved := make(map[string]*string, len(env))
	for k, v := range env {
		if prev, ok := os.LookupEnv(k); ok {
			saved[k] = &prev
		} else {
			saved[k] = nil
		}
		_ = os.Setenv(k, v)
	}
	defer func() {
		for k, prev := range saved {
			if prev == nil {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, *prev)
			}
		}
	}()
	return corecfg.LoadPrefixed(envPrefix, c)
}
