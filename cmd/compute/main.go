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

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/PRO-Robotech/kacho-corelib/operations"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/check"
	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
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

	// authz (E3 / KAC-108): per-RPC OpenFGA Check на public listener'е.
	// AuthZIAMGRPCAddr пуст → interceptor НЕ навешивается (graceful start без
	// kacho-iam в dev). Breakglass=true → interceptor навешивается, но всё
	// пропускает + emit'ит WARN-метрику (dev / emergency).
	publicUnary := []grpc.UnaryServerInterceptor{handler.TenantUnaryInterceptor(false, productionMode)}
	publicStream := []grpc.StreamServerInterceptor{handler.TenantStreamInterceptor(false, productionMode)}

	var authzConn *grpc.ClientConn
	if cfg.AuthZIAMGRPCAddr != "" {
		authzConn, err = dialPeer(cfg.AuthZIAMGRPCAddr, cfg.AuthZIAMTLS)
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
	registerPublicServices(grpcSrv, svcs, opsRepo)
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
		// KAC-106 (E1): TLS-check switched to IAM peer; ResourceManager — legacy fallback.
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
// на kacho-iam.ProjectService.Get. ResourceManagerGRPCAddr оставлен как
// fallback для плавного обновления helm-чартов.
func dialPeers(cfg config.Config, logger *slog.Logger) (service.ProjectClient, service.VPCClient, []*grpc.ClientConn, error) {
	if cfg.SkipPeerValidation {
		logger.Warn("KACHO_COMPUTE_SKIP_PEER_VALIDATION=true — cross-service existence-check disabled (dev/test only)")
		return clients.NoopProjectClient{}, clients.NoopVPCClient{}, nil, nil
	}
	iamAddr := cfg.IAMGRPCAddr
	iamTLS := cfg.IAMTLS
	if cfg.ResourceManagerGRPCAddr != "" && iamAddr == "kacho-iam.kacho.svc.cluster.local:9090" {
		// Backward-compat: helm not yet upgraded — caller set RM address.
		logger.Warn("KACHO_COMPUTE_RESOURCE_MANAGER_GRPC_ADDR set; falling back to RM peer (E1 transitional)",
			"rm_addr", cfg.ResourceManagerGRPCAddr)
		iamAddr = cfg.ResourceManagerGRPCAddr
		iamTLS = cfg.ResourceManagerTLS
	}
	iamConn, err := dialPeer(iamAddr, iamTLS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial iam: %w", err)
	}
	vpcConn, err := dialPeer(cfg.VPCGRPCAddr, cfg.VPCTLS)
	if err != nil {
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("dial vpc: %w", err)
	}
	vpcInternalConn, err := dialPeer(cfg.VPCInternalGRPCAddr, cfg.VPCInternalTLS)
	if err != nil {
		_ = vpcConn.Close()
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("dial vpc internal: %w", err)
	}
	return clients.NewProjectClient(iamConn), clients.NewVPCClient(vpcConn, vpcInternalConn), []*grpc.ClientConn{iamConn, vpcConn, vpcInternalConn}, nil
}

func dialPeer(addr string, useTLS bool) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		creds = insecure.NewCredentials()
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
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
func registerPublicServices(srv *grpc.Server, svcs *services, opsRepo operations.Repo) {
	computev1.RegisterDiskServiceServer(srv, handler.NewDiskHandler(svcs.disk))
	computev1.RegisterImageServiceServer(srv, handler.NewImageHandler(svcs.image))
	computev1.RegisterSnapshotServiceServer(srv, handler.NewSnapshotHandler(svcs.snapshot))
	computev1.RegisterDiskTypeServiceServer(srv, handler.NewDiskTypeHandler(svcs.diskType))
	computev1.RegisterZoneServiceServer(srv, handler.NewZoneHandler(svcs.zone))
	computev1.RegisterRegionServiceServer(srv, handler.NewRegionHandler(svcs.region))
	computev1.RegisterInstanceServiceServer(srv, handler.NewInstanceHandler(svcs.instance))
	operationpb.RegisterOperationServiceServer(srv, handler.NewOperationHandler(opsRepo))
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
