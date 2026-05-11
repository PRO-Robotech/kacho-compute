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

	folderClient, vpcClient, closers, err := dialPeers(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	svcs := buildServices(pool, folderClient, vpcClient, opsRepo, cfg.SkipPeerValidation)

	// Публичный listener — requireAdmin=false; internal :9091 — requireAdmin=true
	// (defense-in-depth поверх NetworkPolicy в helm). Зеркалит kacho-vpc.
	grpcSrv := grpcsrv.NewServer(
		grpc.ChainUnaryInterceptor(handler.TenantUnaryInterceptor(false, productionMode)),
		grpc.ChainStreamInterceptor(handler.TenantStreamInterceptor(false, productionMode)),
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
		if !cfg.ResourceManagerTLS || !cfg.VPCTLS {
			return false, fmt.Errorf("production-strict mode: KACHO_COMPUTE_RESOURCE_MANAGER_TLS=true and KACHO_COMPUTE_VPC_TLS=true required")
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
		if !cfg.ResourceManagerTLS {
			logger.Warn("KACHO_COMPUTE_RESOURCE_MANAGER_TLS=false — cross-service gRPC plaintext (dev only)")
		}
		if cfg.DBSSLMode == "" || cfg.DBSSLMode == "disable" {
			logger.Warn("KACHO_COMPUTE_DB_SSLMODE=disable — DB plaintext (dev only)")
		}
	}
	return productionMode, nil
}

// dialPeers открывает gRPC-клиенты к peer-сервисам (resource-manager, vpc) либо
// возвращает no-op-заглушки при KACHO_COMPUTE_SKIP_PEER_VALIDATION=true.
func dialPeers(cfg config.Config, logger *slog.Logger) (service.FolderClient, service.VPCClient, []*grpc.ClientConn, error) {
	if cfg.SkipPeerValidation {
		logger.Warn("KACHO_COMPUTE_SKIP_PEER_VALIDATION=true — cross-service existence-check disabled (dev/test only)")
		return clients.NoopFolderClient{}, clients.NoopVPCClient{}, nil, nil
	}
	rmConn, err := dialPeer(cfg.ResourceManagerGRPCAddr, cfg.ResourceManagerTLS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial resource-manager: %w", err)
	}
	vpcConn, err := dialPeer(cfg.VPCGRPCAddr, cfg.VPCTLS)
	if err != nil {
		_ = rmConn.Close()
		return nil, nil, nil, fmt.Errorf("dial vpc: %w", err)
	}
	return clients.NewFolderClient(rmConn), clients.NewVPCClient(vpcConn), []*grpc.ClientConn{rmConn, vpcConn}, nil
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
// skipIPAM (== cfg.SkipPeerValidation) → Instance NIC-ам выдаются синтетические
// IP вместо реальных, выделенных через kacho-vpc IPAM.
func buildServices(pool *pgxpool.Pool, folderClient service.FolderClient, vpcClient service.VPCClient, opsRepo operations.Repo, skipIPAM bool) *services {
	diskRepo := repo.NewDiskRepo(pool)
	imageRepo := repo.NewImageRepo(pool)
	snapshotRepo := repo.NewSnapshotRepo(pool)
	instanceRepo := repo.NewInstanceRepo(pool)
	diskTypeRepo := repo.NewDiskTypeRepo(pool)
	zoneRepo := repo.NewZoneRepo(pool)

	diskTypeSvc := service.NewDiskTypeService(diskTypeRepo)
	zoneSvc := service.NewZoneService(zoneRepo)
	return &services{
		disk:     service.NewDiskService(diskRepo, imageRepo, snapshotRepo, diskTypeRepo, zoneRepo, folderClient, opsRepo),
		image:    service.NewImageService(imageRepo, diskRepo, snapshotRepo, folderClient, opsRepo),
		snapshot: service.NewSnapshotService(snapshotRepo, diskRepo, folderClient, opsRepo),
		diskType: diskTypeSvc,
		zone:     zoneSvc,
		instance: service.NewInstanceService(instanceRepo, diskRepo, imageRepo, snapshotRepo, zoneRepo, folderClient, vpcClient, opsRepo, skipIPAM),
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
	computev1.RegisterInstanceServiceServer(srv, handler.NewInstanceHandler(svcs.instance))
	operationpb.RegisterOperationServiceServer(srv, handler.NewOperationHandler(opsRepo))
}

// registerInternalServices — kacho-only/admin RPC на internal listener (:9091,
// не маршрутизируется наружу; NetworkPolicy + requireAdmin-interceptor).
func registerInternalServices(srv *grpc.Server, svcs *services, pool *pgxpool.Pool, dsn string, logger *slog.Logger, watchMaxStreams int) {
	computev1.RegisterInternalWatchServiceServer(srv, handler.NewInternalWatchHandler(pool, dsn, logger.With("component", "internal-watch"), watchMaxStreams))
	computev1.RegisterInternalDiskTypeServiceServer(srv, handler.NewInternalDiskTypeHandler(svcs.diskType))
	computev1.RegisterInternalZoneServiceServer(srv, handler.NewInternalZoneHandler(svcs.zone))
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
