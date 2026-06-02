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

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/check"
	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/fgawrite"
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

	// KAC-188 follow-up: write-side FGA. fgaTupleWriter — nil unless
	// authz.tuple-write is configured; nil makes fgawrite.Emit a no-op
	// (dev / degraded). When wired, each compute resource Create publishes
	// `compute_<resource>:<id>#project@project:<project_id>` so a per-resource
	// FGA Check resolves through the `<rel> from project` cascade. Without this
	// every Get/Update/Delete on a freshly created Instance/Disk/Image/Snapshot
	// fails closed with `no path` (the live bug that motivated KAC-188 follow-up
	// on compute_instance:epd5hd7gadv28tny6246).
	fgaTupleWriter := buildFGATupleWriter(cfg, logger)

	svcs := buildServices(pool, projectClient, vpcClient, opsRepo, cfg.SkipPeerValidation)
	if fgaTupleWriter != nil {
		svcs.instance.WithFGAWriter(fgaTupleWriter, logger)
		svcs.disk.WithFGAWriter(fgaTupleWriter, logger)
		svcs.image.WithFGAWriter(fgaTupleWriter, logger)
		svcs.snapshot.WithFGAWriter(fgaTupleWriter, logger)
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
		authzConn, err = dialPeer(cfg.AuthZIAMGRPCAddr, cfg.AuthZIAMTLS, true)
		if err != nil {
			return fmt.Errorf("dial kacho-iam (authz): %w", err)
		}
		defer authzConn.Close()
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

	// Публичный listener — requireAdmin=false; internal :9091 — requireAdmin=true
	// (defense-in-depth поверх NetworkPolicy в helm). Зеркалит kacho-vpc.
	grpcSrv := grpcsrv.NewServer(
		grpc.ChainUnaryInterceptor(publicUnary...),
		grpc.ChainStreamInterceptor(publicStream...),
	)
	internalSrv := grpcsrv.NewServer(
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
	iamConn, err := dialPeer(cfg.IAMGRPCAddr, cfg.IAMTLS, false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial iam: %w", err)
	}
	vpcConn, err := dialPeer(cfg.VPCGRPCAddr, cfg.VPCTLS, false)
	if err != nil {
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("dial vpc: %w", err)
	}
	vpcInternalConn, err := dialPeer(cfg.VPCInternalGRPCAddr, cfg.VPCInternalTLS, false)
	if err != nil {
		_ = vpcConn.Close()
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("dial vpc internal: %w", err)
	}
	return clients.NewProjectClient(iamConn), clients.NewVPCClient(vpcConn, vpcInternalConn), []*grpc.ClientConn{iamConn, vpcConn, vpcInternalConn}, nil
}

// peerKeepalive — keepalive-параметры для peer-conn (KAC-244). idle=true для
// преимущественно-idle conn'ов (authz → iam-internal): PermitWithoutStream держит
// conn тёплым пингами без активных стримов, прямо лечит half-open-столл.
func peerKeepalive(idle bool) keepalive.ClientParameters {
	return grpcclient.KeepaliveParams(idle)
}

// peerDialOpts — seam-функция (тестируемая): собирает []grpc.DialOption для
// dialPeer (creds по useTLS + keepalive по idle). Вынесена отдельно, т.к.
// grpc.NewClient не отдаёт опции назад — тест инспектирует именно этот набор.
func peerDialOpts(useTLS, idle bool) []grpc.DialOption {
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		creds = insecure.NewCredentials()
	}
	return []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpcclient.KeepaliveDialOption(idle),
	}
}

// dialPeer открывает gRPC-conn к peer-сервису с keepalive (KAC-244). idle=true →
// idle-prone conn (authz/internal): пинги без стрима держат его тёплым.
func dialPeer(addr string, useTLS, idle bool) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, peerDialOpts(useTLS, idle)...)
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

// buildFGATupleWriter — KAC-188 follow-up. Возвращает fgawrite.HierarchyTupleWriter,
// готовый к подвешиванию в Create use-cases (Instance/Disk/Image/Snapshot).
// Если master-switch отключён (KACHO_COMPUTE_AUTHZ_TUPLE_WRITE_ENABLED=false)
// либо endpoint/store-id пустые — возвращает nil, что делает fgawrite.Emit
// no-op'ом (dev/degraded). В production-режиме без выписки tuple'ов каждый
// per-resource Check валится с `no path` — это live bug, который мы чиним.
func buildFGATupleWriter(cfg config.Config, logger *slog.Logger) fgawrite.HierarchyTupleWriter {
	if !cfg.AuthZTupleWriteEnabled {
		logger.Warn("compute write-side FGA NOT wired — KACHO_COMPUTE_AUTHZ_TUPLE_WRITE_ENABLED=false; " +
			"created resources will have no per-resource FGA hierarchy tuple (KAC-188 follow-up)")
		return nil
	}
	if cfg.AuthZTupleWriteOpenFGAEndpoint == "" || cfg.AuthZTupleWriteStoreID == "" {
		logger.Warn("compute write-side FGA NOT wired — endpoint or store-id empty " +
			"(KACHO_COMPUTE_AUTHZ_TUPLE_WRITE_OPENFGA_ENDPOINT / _STORE_ID); created resources " +
			"will have no per-resource FGA hierarchy tuple (KAC-188 follow-up)")
		return nil
	}
	timeout := time.Duration(cfg.AuthZTupleWriteTimeoutMs) * time.Millisecond
	logger.Info("compute write-side FGA wired (KAC-188 follow-up)",
		"openfga_endpoint", cfg.AuthZTupleWriteOpenFGAEndpoint,
		"store_id", cfg.AuthZTupleWriteStoreID,
		"model_id", cfg.AuthZTupleWriteModelID,
		"timeout", timeout,
	)
	return &clients.OpenFGAWriteClient{
		Endpoint: cfg.AuthZTupleWriteOpenFGAEndpoint,
		StoreID:  cfg.AuthZTupleWriteStoreID,
		ModelID:  cfg.AuthZTupleWriteModelID,
		Timeout:  timeout,
	}
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
