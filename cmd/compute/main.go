package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/check"
	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/handler"
	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: compute {serve|migrate up|migrate down|migrate status}")
	}
	cmd := os.Args[1]

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	switch cmd {
	case "migrate":
		if len(os.Args) < 3 {
			log.Fatal("usage: compute migrate {up|down|status}")
		}
		runMigrate(cfg, os.Args[2])
	case "serve":
		if err := runServe(cfg); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown command: %s", cmd)
	}
}

// services — собранный набор бизнес-сервисов (composition-point).
type services struct {
	disk     *service.DiskService
	image    *service.ImageService
	snapshot *service.SnapshotService
	diskType *service.DiskTypeService
	zone     *service.ZoneService
	region   *service.RegionService
	instance *service.InstanceService
}

func runServe(cfg config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	logger := observability.NewSlogger(os.Stdout)
	slog.SetDefault(logger)

	productionMode, err := validateAuthMode(cfg, logger)
	if err != nil {
		return err
	}

	pool, err := coredb.NewPool(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()

	opsRepo := operations.NewRepo(pool, "public")

	projectClient, vpcClient, closers, err := dialPeers(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	svcs := buildServices(pool, projectClient, vpcClient, opsRepo, cfg.SkipPeerValidation)

	// SEC-D: register-drainer — applies FGA owner-tuple register/unregister intents
	// (compute_fga_register_outbox, written transactionally by repo.Insert/Delete)
	// via kacho-iam InternalIAMService.RegisterResource/UnregisterResource over the
	// (optionally mTLS) compute→iam edge. Idempotent + retry-on-Unavailable; the
	// owner-tuple is never lost (closes GitHub Issue N5 — best-effort dual-write).
	// Default-on (OQ-SEC-D-5); without it created resources get no per-resource FGA
	// tuple. The drainer dial-conn lives for the process lifetime (closed by ctx).
	if cfg.FGARegisterDrainerEnabled {
		if drainCloser, derr := startRegisterDrainer(ctx, cfg, pool, logger); derr != nil {
			return fmt.Errorf("start register-drainer: %w", derr)
		} else if drainCloser != nil {
			defer drainCloser()
		}
	} else {
		logger.Warn("FGA register-drainer DISABLED (KACHO_COMPUTE_FGA_REGISTER_DRAINER_ENABLED=false) — " +
			"created resources will not get their per-resource FGA owner-tuple registered in IAM")
	}

	// KAC-178 §2 (W1.4 mirror of kacho-vpc): principal-extract ОБЯЗАН стоять
	// ПЕРВЫМ в public-цепочке — без него operations.PrincipalFromContext(ctx)
	// возвращает SystemPrincipal() = user:bootstrap для каждого request'а,
	// независимо от того, что api-gateway форвардит x-kacho-principal-* через
	// gRPC metadata. UnaryPrincipalExtract читает headers и кладёт реальный
	// operations.Principal в ctx → authz-interceptor + use-case'ы, пишущие
	// operations.principal_* колонки (Operation.created_by), видят верного
	// principal'а вместо anonymous/bootstrap. Mirror of kacho-vpc/cmd/vpc/main.go.
	//
	// authz (E3 / KAC-108): per-RPC OpenFGA Check на public listener'е.
	// AuthZIAMGRPCAddr пуст → interceptor НЕ навешивается (graceful start без
	// kacho-iam в dev). Breakglass=true → interceptor навешивается, но всё
	// пропускает + emit'ит WARN-метрику (dev / emergency).
	publicUnary := []grpc.UnaryServerInterceptor{
		grpcsrv.UnaryPrincipalExtract(),
		handler.TenantUnaryInterceptor(false, productionMode),
	}
	publicStream := []grpc.StreamServerInterceptor{
		grpcsrv.StreamPrincipalExtract(),
		handler.TenantStreamInterceptor(false, productionMode),
	}

	var authzConn *grpc.ClientConn
	if cfg.AuthZIAMGRPCAddr != "" {
		// authz-conn → iam-internal:9091 — idle-prone (между всплесками authz Check
		// активных стримов нет) → idle=true: пинги держат conn тёплым (KAC-244).
		//
		// SEC-I: per-RPC InternalIAMService.Check + FGA-filtered List (этот conn
		// шарится с list-filter через buildListFilter) предъявляют client-cert mTLS
		// через cfg.IAMAuthzMTLS (enable=false → insecure dev; enable=true без
		// валидного cert-trio → startup error, fail-closed). Заменяет server-auth-only
		// cfg.AuthZIAMTLS bool, который не предъявлял cert (падал бы под SEC-H).
		authzCreds, cerr := grpcclient.TLSClientTransportCreds(cfg.IAMAuthzMTLS)
		if cerr != nil {
			return fmt.Errorf("compute→iam Check/list-filter mTLS creds: %w", cerr)
		}
		authzConn, err = dialPeerCreds(cfg.AuthZIAMGRPCAddr, authzCreds, true)
		if err != nil {
			return fmt.Errorf("dial kacho-iam (authz): %w", err)
		}
		defer authzConn.Close()
		logger.Info("compute→iam read/authz mTLS state",
			"project_get_mtls", cfg.IAMProjectMTLS.Enable,
			"authz_check_listfilter_mtls", cfg.IAMAuthzMTLS.Enable,
		)
	}
	authzIntr, err := check.NewInterceptor(check.Options{
		ServiceName: "kacho-compute",
		IAMConn:     authzConn,
		Breakglass:  cfg.AuthZBreakglass,
		Logger:      logger,
	})
	switch {
	case err == nil && authzIntr != nil:
		publicUnary = append(publicUnary, authzIntr.Unary())
		publicStream = append(publicStream, authzIntr.Stream())
		logger.Info("authz interceptor enabled",
			"iam_endpoint", cfg.AuthZIAMGRPCAddr,
			"breakglass", cfg.AuthZBreakglass,
		)
	case errors.Is(err, check.ErrIAMConnNotConfigured):
		// Dev — продолжаем без authz-interceptor'а (scope-guard KAC-108).
		logger.Warn("authz interceptor NOT enabled — KACHO_COMPUTE_AUTHZ_IAM_GRPC_ADDR not configured (dev mode)")
	case err != nil:
		return fmt.Errorf("build authz interceptor: %w", err)
	}

	// KAC-127 Phase 4: FGA-filtered List handlers. Build the filter from
	// configurable env vars (ListFilter*). If iam-authz conn is unavailable
	// (dev / breakglass), filter is nil → handler bypasses FGA filtering.
	listFilter := buildListFilter(cfg, authzConn, logger)

	// SEC-D: opt-in mTLS server-creds per listener (enable=false → insecure, dev
	// backward-compat). Public :9090 + internal :9091 have independent TLSServer
	// configs (PUBLIC_SERVER_MTLS / INTERNAL_SERVER_MTLS); enable=true with an
	// unreadable cert / empty client-CA → fail-closed error (no silent insecure).
	publicCreds, err := cfg.PublicServerCreds()
	if err != nil {
		return fmt.Errorf("public listener tls creds: %w", err)
	}
	internalCreds, err := cfg.InternalServerCreds()
	if err != nil {
		return fmt.Errorf("internal listener tls creds: %w", err)
	}

	// Публичный listener — requireAdmin=false; internal :9091 — requireAdmin=true
	// (defense-in-depth поверх NetworkPolicy в helm). Зеркалит kacho-vpc.
	grpcSrv := grpcsrv.NewServer(
		publicCreds,
		grpc.ChainUnaryInterceptor(publicUnary...),
		grpc.ChainStreamInterceptor(publicStream...),
	)
	internalSrv := grpcsrv.NewServer(
		internalCreds,
		grpc.ChainUnaryInterceptor(handler.TenantUnaryInterceptor(true, productionMode)),
		grpc.ChainStreamInterceptor(handler.TenantStreamInterceptor(true, productionMode)),
	)
	registerPublicServices(grpcSrv, svcs, opsRepo, listFilter)
	registerInternalServices(internalSrv, svcs, pool, cfg.MigrateDSN(), logger, cfg.WatchMaxStreams)

	listener, err := net.Listen("tcp", ":"+cfg.GrpcPort)
	if err != nil {
		return err
	}
	internalListener, err := net.Listen("tcp", ":"+cfg.InternalGrpcPort)
	if err != nil {
		_ = listener.Close()
		return err
	}
	logger.Info("kacho-compute listening", "public_port", cfg.GrpcPort, "internal_port", cfg.InternalGrpcPort)

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		internalSrv.GracefulStop()
		grpcSrv.GracefulStop()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer drainCancel()
		if err := operations.Wait(drainCtx); err != nil {
			logger.Warn("operations workers did not finish in time", "err", err, "active", operations.Active())
		}
	}()

	go func() {
		if err := internalSrv.Serve(internalListener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logger.Error("internal grpc server stopped", "err", err)
		}
	}()

	serveErr := grpcSrv.Serve(listener)
	cancel()
	<-shutdownDone
	return serveErr
}

// validateAuthMode разбирает KACHO_COMPUTE_AUTH_MODE (whitelist), для
// production-strict валидирует cross-service TLS + DB sslmode, логирует insecure
// dev-defaults. Зеркалит kacho-vpc/cmd/vpc/main.go::validateAuthMode.
func validateAuthMode(cfg config.Config, logger *slog.Logger) (productionMode bool, err error) {
	switch cfg.AuthMode {
	case "dev":
		productionMode = false
	case "production":
		productionMode = true
		logger.Warn("AuthMode=production: anonymous callers will be rejected")
	case "production-strict":
		productionMode = true
		// KAC-106 (E1): TLS-check on IAM peer (KAC-127 dropped RM legacy fallback).
		if !cfg.IAMTLS || !cfg.VPCTLS {
			return false, fmt.Errorf("production-strict mode: KACHO_COMPUTE_IAM_TLS=true and KACHO_COMPUTE_VPC_TLS=true required")
		}
		switch cfg.DBSSLMode {
		case "require", "verify-ca", "verify-full":
		default:
			return false, fmt.Errorf("production-strict mode: KACHO_COMPUTE_DB_SSLMODE must be one of require|verify-ca|verify-full (got %q)", cfg.DBSSLMode)
		}
		logger.Warn("AuthMode=production-strict: anonymous rejected + TLS+SSL strictly validated")
	default:
		return false, fmt.Errorf("unknown KACHO_COMPUTE_AUTH_MODE=%q (allowed: dev, production, production-strict)", cfg.AuthMode)
	}
	if !productionMode {
		if !cfg.IAMTLS {
			logger.Warn("KACHO_COMPUTE_IAM_TLS=false — cross-service gRPC plaintext (dev only)")
		}
		if cfg.DBSSLMode == "" || cfg.DBSSLMode == "disable" {
			logger.Warn("KACHO_COMPUTE_DB_SSLMODE=disable — DB plaintext (dev only)")
		}
	}
	return productionMode, nil
}

// dialPeers открывает gRPC-клиенты к peer-сервисам (kacho-iam, vpc — два
// conn'а: публичный :9090 и internal :9091 для InternalZoneService) либо
// возвращает no-op-заглушки при KACHO_COMPUTE_SKIP_PEER_VALIDATION=true.
//
// KAC-106 (E1): project-existence-check переключён с kacho-resource-manager
// на kacho-iam.ProjectService.Get. KAC-127: legacy RM-fallback удалён.
func dialPeers(cfg config.Config, logger *slog.Logger) (service.ProjectClient, service.VPCClient, []*grpc.ClientConn, error) {
	if cfg.SkipPeerValidation {
		logger.Warn("KACHO_COMPUTE_SKIP_PEER_VALIDATION=true — cross-service existence-check disabled (dev/test only)")
		return clients.NoopProjectClient{}, clients.NoopVPCClient{}, nil, nil
	}
	// iam (public ProjectService.Get) + vpc conn'ы — активно используются на
	// request-path каждой мутации → idle=false (трафик есть, idle-пинги не нужны;
	// keepalive всё равно ставится для half-open-detection при паузах). KAC-244.
	//
	// SEC-I: compute→iam ProjectService.Get (:9090) предъявляет client-cert mTLS
	// через cfg.IAMProjectMTLS (enable=false → insecure dev; enable=true без
	// валидного cert-trio → startup error, fail-closed). Заменяет server-auth-only
	// cfg.IAMTLS bool, который не предъявлял cert (падал бы под SEC-H).
	iamCreds, err := grpcclient.TLSClientTransportCreds(cfg.IAMProjectMTLS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compute→iam ProjectService.Get mTLS creds: %w", err)
	}
	iamConn, err := dialPeerCreds(cfg.IAMGRPCAddr, iamCreds, false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial iam: %w", err)
	}
	// SEC-M: compute→vpc resource-creation edge (Subnet/SecurityGroup/Address.Get
	// NIC-spec validation on :9090 + one-to-one-NAT IPAM on :9091) presents a
	// client-cert via cfg.VPCMTLS (enable=false → insecure dev; enable=true without a
	// valid cert-trio → startup error, fail-closed). Mirror of the already-shipped
	// vpc→compute branch (mtlsCfg.ComputeMTLS.Enable) and the SEC-I iam edges; replaces
	// the server-auth-only bools cfg.VPCTLS / cfg.VPCInternalTLS, which presented no
	// client-cert (both dials would fail once kacho-vpc runs RequireAndVerifyClientCert).
	// BOTH vpc-listeners share one Service-host `vpc` → one cfg.VPCMTLS / one
	// ServerName covers both ports (M6 / B-04; contrast iam SEC-I per-listener split).
	vpcConn, err := dialVPCPeer(cfg, cfg.VPCGRPCAddr, cfg.VPCTLS)
	if err != nil {
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("dial vpc: %w", err)
	}
	vpcInternalConn, err := dialVPCPeer(cfg, cfg.VPCInternalGRPCAddr, cfg.VPCInternalTLS)
	if err != nil {
		_ = vpcConn.Close()
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("dial vpc internal: %w", err)
	}
	logger.Info("compute→vpc resource-creation mTLS state",
		"nic_spec_ipam_mtls", cfg.VPCMTLS.Enable,
	)
	return clients.NewProjectClient(iamConn), clients.NewVPCClient(vpcConn, vpcInternalConn), []*grpc.ClientConn{iamConn, vpcConn, vpcInternalConn}, nil
}

// dialVPCPeer dials a kacho-vpc listener (public :9090 NIC-spec or internal :9091
// IPAM) for the compute→vpc resource-creation edge. When cfg.VPCMTLS.Enable it
// presents the kacho-compute-client-tls client-cert via the per-edge cfg.VPCMTLS
// helper (SEC-M, fail-closed on a bad trio); otherwise it keeps the legacy
// server-auth-only bool path (dev backward-compat, zero regression). Both listeners
// share one cfg.VPCMTLS (one cert-trio, one ServerName=vpc.* dial-host — M6 / B-04).
// vpc-conns are request-path-active (idle=false; keepalive still set for
// half-open-detection on pauses, KAC-244).
func dialVPCPeer(cfg config.Config, addr string, legacyTLS bool) (*grpc.ClientConn, error) {
	if cfg.VPCMTLS.Enable {
		creds, err := grpcclient.TLSClientTransportCreds(cfg.VPCMTLS)
		if err != nil {
			return nil, fmt.Errorf("compute→vpc NIC-spec/IPAM mTLS creds: %w", err)
		}
		return dialPeerCreds(addr, creds, false)
	}
	return dialPeer(addr, legacyTLS, false)
}

// peerKeepalive — keepalive-параметры для peer-conn (KAC-244). idle=true для
// преимущественно-idle conn'ов (authz → iam-internal): PermitWithoutStream держит
// conn тёплым пингами без активных стримов, прямо лечит half-open-столл.
func peerKeepalive(idle bool) keepalive.ClientParameters {
	return grpcclient.KeepaliveParams(idle)
}

// peerDialOptsCreds — seam-функция (тестируемая): собирает []grpc.DialOption из
// готовых transport-creds + keepalive по idle. Единая точка для обоих путей:
// legacy bool-peer (peerDialOpts) и per-edge client-cert mTLS iam-рёбер (SEC-I),
// которые резолвят creds через corelib grpcclient.TLSClientTransportCreds. grpc.NewClient
// не отдаёт опции назад — тест инспектирует именно этот набор.
func peerDialOptsCreds(creds credentials.TransportCredentials, idle bool) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpcclient.KeepaliveDialOption(idle),
	}
}

// peerDialOpts — legacy server-auth-only seam (bool useTLS) для НЕ-iam пиров (vpc
// public/internal до их SEC-D/SEC-I-миграции). Делегирует в peerDialOptsCreds,
// сохраняя bool-контракт существующего dialpeer_test.go.
func peerDialOpts(useTLS, idle bool) []grpc.DialOption {
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		creds = insecure.NewCredentials()
	}
	return peerDialOptsCreds(creds, idle)
}

// dialPeer открывает gRPC-conn к peer-сервису с keepalive (KAC-244, legacy bool
// useTLS). idle=true → idle-prone conn (authz/internal): пинги без стрима держат
// его тёплым.
func dialPeer(addr string, useTLS, idle bool) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, peerDialOpts(useTLS, idle)...)
}

// dialPeerCreds открывает gRPC-conn к peer-сервису, предъявляя готовые transport-
// creds (SEC-I per-edge client-cert mTLS). Используется для compute→iam read/authz
// рёбер: creds резолвятся из cfg.IAMProjectMTLS / cfg.IAMAuthzMTLS через corelib
// grpcclient (enable=false → insecure, dev backward-compat).
func dialPeerCreds(addr string, creds credentials.TransportCredentials, idle bool) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, peerDialOptsCreds(creds, idle)...)
}

// buildServices создаёт все repo'ы поверх pool и собирает из них бизнес-сервисы.
// kacho-compute — owner Geography (Region/Zone): зоны/регионы всегда читаются из
// локальных таблиц `zones`/`regions`, никакого proxy в kacho-vpc (эпик KAC-15).
// skipPeer (== cfg.SkipPeerValidation) теперь влияет только на VPC NIC-IPAM/SG
// existence-check (folder/subnet/sg), но не на geography.
func buildServices(pool *pgxpool.Pool, projectClient service.ProjectClient, vpcClient service.VPCClient, opsRepo operations.Repo, skipPeer bool) *services {
	diskRepo := repo.NewDiskRepo(pool)
	imageRepo := repo.NewImageRepo(pool)
	snapshotRepo := repo.NewSnapshotRepo(pool)
	instanceRepo := repo.NewInstanceRepo(pool)
	diskTypeRepo := repo.NewDiskTypeRepo(pool)
	zoneRepo := repo.NewZoneRepo(pool)
	regionRepo := repo.NewRegionRepo(pool)

	// Источник зон для existence-check zone_id (Disk/Instance Create, Disk Relocate)
	// — локальная таблица `zones` (compute — owner Geography).
	zoneRegistry := repo.NewZoneRepoSource(zoneRepo)

	diskTypeSvc := service.NewDiskTypeService(diskTypeRepo)
	return &services{
		disk:     service.NewDiskService(diskRepo, imageRepo, snapshotRepo, diskTypeRepo, zoneRegistry, projectClient, opsRepo),
		image:    service.NewImageService(imageRepo, diskRepo, snapshotRepo, projectClient, opsRepo),
		snapshot: service.NewSnapshotService(snapshotRepo, diskRepo, projectClient, opsRepo),
		diskType: diskTypeSvc,
		zone:     service.NewZoneService(zoneRepo),
		region:   service.NewRegionService(regionRepo),
		instance: service.NewInstanceService(instanceRepo, diskRepo, imageRepo, snapshotRepo, zoneRegistry, projectClient, vpcClient, opsRepo, skipPeer),
	}
}

// registerPublicServices — публичные (verbatim-YC) RPC + OperationService на
// внешний listener (:9090, проксируется api-gateway).
//
// KAC-127 Phase 4: List handlers получают listFilter (FGA filter); может быть
// nil — тогда FGA-фильтрация на List отключена (dev/breakglass). Catalog
// (DiskType/Zone/Region) — public read, FGA bypass not needed (handler skips).
func registerPublicServices(srv *grpc.Server, svcs *services, opsRepo operations.Repo, listFilter authzfilter.Filter) {
	computev1.RegisterDiskServiceServer(srv, handler.NewDiskHandler(svcs.disk, listFilter))
	computev1.RegisterImageServiceServer(srv, handler.NewImageHandler(svcs.image, listFilter))
	computev1.RegisterSnapshotServiceServer(srv, handler.NewSnapshotHandler(svcs.snapshot, listFilter))
	computev1.RegisterDiskTypeServiceServer(srv, handler.NewDiskTypeHandler(svcs.diskType))
	computev1.RegisterZoneServiceServer(srv, handler.NewZoneHandler(svcs.zone))
	computev1.RegisterRegionServiceServer(srv, handler.NewRegionHandler(svcs.region))
	computev1.RegisterInstanceServiceServer(srv, handler.NewInstanceHandler(svcs.instance, listFilter))
	operationpb.RegisterOperationServiceServer(srv, handler.NewOperationHandler(opsRepo))
}

// buildListFilter — KAC-127 Phase 4. Возвращает authzfilter.Filter, готовый к
// подвешиванию в public List handlers. Если KACHO_COMPUTE_LIST_FILTER_ENABLED=false
// либо authzConn=nil (dev без iam) — возвращает nil, что означает «handler
// делает bypass FGA filter и возвращает всё подряд». Production-strict
// валидация — выше (validateAuthMode).
func buildListFilter(cfg config.Config, authzConn *grpc.ClientConn, logger *slog.Logger) authzfilter.Filter {
	if !cfg.ListFilterEnabled {
		logger.Info("list filter disabled (KACHO_COMPUTE_LIST_FILTER_ENABLED=false)")
		return nil
	}
	if authzConn == nil {
		logger.Warn("list filter requested but KACHO_COMPUTE_AUTHZ_IAM_GRPC_ADDR is unset — disabled")
		return nil
	}
	cli := authzfilter.NewIAMAuthorizeClient(authzConn)
	cacheMax := cfg.ListFilterCacheMaxEntries
	if cacheMax <= 0 {
		cacheMax = 10000
	}
	fcfg := authzfilter.Config{
		Enabled:         true,
		Timeout:         time.Duration(cfg.ListFilterTimeoutMs) * time.Millisecond,
		CacheTTL:        time.Duration(cfg.ListFilterCacheTTLMs) * time.Millisecond,
		CacheMaxEntries: cacheMax,
		FailOpen:        cfg.ListFilterFailOpen,
	}
	logger.Info("list filter enabled",
		"timeout_ms", cfg.ListFilterTimeoutMs,
		"cache_ttl_ms", cfg.ListFilterCacheTTLMs,
		"cache_max_entries", cacheMax,
		"fail_open", cfg.ListFilterFailOpen,
	)
	return authzfilter.NewFGAFilter(cli, fcfg)
}

// startRegisterDrainer — SEC-D. Dials the kacho-iam internal endpoint over the
// compute→iam edge (mTLS opt-in via cfg.IAMRegisterClientCreds — enable=false →
// insecure dev) and starts a corelib outbox/drainer over
// compute_fga_register_outbox. Each pending intent is replayed through
// InternalIAMService.RegisterResource / UnregisterResource by the applier
// (idempotent; Unavailable → retry with backoff; InvalidArgument → poison). The
// drainer Run-loop owns claim-CAS + advisory-lock for exactly-once across replicas
// (corelib W1.1). Returns a closer that shuts the dial-conn; nil error on success.
//
// The drainer dials the iam-internal :9091 listener — RegisterResource is an
// Internal-only RPC (ban #6); the addr is derived from AuthZIAMGRPCAddr (the
// existing iam-internal endpoint compute already uses for Check) and falls back to
// IAMGRPCAddr when unset.
func startRegisterDrainer(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, logger *slog.Logger) (func(), error) {
	addr := cfg.AuthZIAMGRPCAddr
	if addr == "" {
		addr = cfg.IAMGRPCAddr
	}

	creds, err := cfg.IAMRegisterClientCreds()
	if err != nil {
		return nil, fmt.Errorf("compute→iam register mTLS creds: %w", err)
	}
	// idle-prone edge (register-drainer is mostly waiting on NOTIFY) → keepalive
	// idle pings keep the conn warm (KAC-244).
	conn, err := grpc.NewClient(addr, creds, grpcclient.KeepaliveDialOption(true))
	if err != nil {
		return nil, fmt.Errorf("dial kacho-iam (register-drainer): %w", err)
	}

	applier := clients.NewIAMRegisterApplier(conn)
	d, err := drainer.New[fgaintent.Payload](
		pool,
		drainer.Config{
			Table:   "public.compute_fga_register_outbox",
			Channel: "compute_fga_register_outbox",
		},
		func(b []byte) (fgaintent.Payload, error) {
			p, derr := fgaintent.Decode(b)
			if derr != nil {
				// Malformed payload — permanent poison, never retried.
				return fgaintent.Payload{}, errors.Join(drainer.ErrPermanent, derr)
			}
			return p, nil
		},
		applier.Apply,
		logger.With("component", "fga-register-drainer"),
	)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("build register-drainer: %w", err)
	}

	go func() {
		if rerr := d.Run(ctx); rerr != nil {
			logger.Error("register-drainer stopped", "err", rerr)
		}
	}()
	logger.Info("FGA register-drainer started",
		"iam_addr", addr, "mtls", cfg.IAMRegisterMTLS.Enable)

	return func() { _ = conn.Close() }, nil
}

// registerInternalServices — kacho-only/admin RPC на internal listener (:9091,
// не маршрутизируется наружу; NetworkPolicy + requireAdmin-interceptor).
func registerInternalServices(srv *grpc.Server, svcs *services, pool *pgxpool.Pool, dsn string, logger *slog.Logger, watchMaxStreams int) {
	computev1.RegisterInternalWatchServiceServer(srv, handler.NewInternalWatchHandler(pool, dsn, logger.With("component", "internal-watch"), watchMaxStreams))
	computev1.RegisterInternalDiskTypeServiceServer(srv, handler.NewInternalDiskTypeHandler(svcs.diskType))
	computev1.RegisterInternalZoneServiceServer(srv, handler.NewInternalZoneHandler(svcs.zone))
	computev1.RegisterInternalRegionServiceServer(srv, handler.NewInternalRegionHandler(svcs.region))
}

func runMigrate(cfg config.Config, direction string) {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("goose dialect: %v", err)
	}
	db, err := sql.Open("pgx", cfg.MigrateDSN())
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var gooseErr error
	switch direction {
	case "up":
		gooseErr = goose.Up(db, ".")
	case "down":
		gooseErr = goose.Down(db, ".")
	case "status":
		gooseErr = goose.Status(db, ".")
	default:
		log.Fatalf("unknown migrate direction: %s", direction)
	}
	if gooseErr != nil {
		log.Fatalf("migrate %s: %v", direction, gooseErr)
	}
}
