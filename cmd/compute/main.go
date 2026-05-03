package main

import (
	"context"
	"database/sql"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/PRO-Robotech/kacho-corelib/outbox"
	"github.com/PRO-Robotech/kacho-corelib/watch"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/handler"
	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
	"github.com/PRO-Robotech/kacho-compute/internal/reconciler"
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

func runServe(cfg config.Config) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	logger := observability.NewSlogger(os.Stdout)

	pool, err := coredb.NewPool(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()

	transactor := coredb.NewTransactor(pool)
	outboxWriter := outbox.NewWriter("kacho_compute")
	hub := watch.NewHub(ctx, pool, "kacho_compute")

	// gRPC клиенты к resource-manager и vpc
	rmConn, err := grpc.NewClient(cfg.ResourceManagerGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer rmConn.Close()

	vpcConn, err := grpc.NewClient(cfg.VPCGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer vpcConn.Close()

	folderClient := clients.NewFolderClient(rmConn)
	subnetClient := clients.NewSubnetClient(vpcConn)

	// Repos
	instanceRepo := repo.NewInstanceRepo(pool, transactor, outboxWriter)
	diskRepo := repo.NewDiskRepo(pool, transactor, outboxWriter)
	imageRepo := repo.NewImageRepo(pool)
	snapshotRepo := repo.NewSnapshotRepo(pool, transactor, outboxWriter)

	// Services
	instanceSvc := service.NewInstanceService(instanceRepo, diskRepo, folderClient, subnetClient)
	diskSvc := service.NewDiskService(diskRepo, imageRepo, folderClient)
	imageSvc := service.NewImageService(imageRepo)
	snapshotSvc := service.NewSnapshotService(snapshotRepo, diskRepo)

	// Reconciler
	instanceHandler := reconciler.NewInstanceHandler(instanceRepo, diskRepo, cfg.Sim)
	diskHandler := reconciler.NewDiskHandler(diskRepo, diskSvc, cfg.Sim)
	snapshotHandler := reconciler.NewSnapshotHandler(snapshotRepo, cfg.Sim)

	dispatcher := reconciler.NewDispatcher(
		pool,
		instanceRepo,
		diskRepo,
		snapshotRepo,
		instanceHandler,
		diskHandler,
		snapshotHandler,
		slog.Default(),
	)

	go watch.RunCleanup(ctx, pool, "compute")
	go dispatcher.Run(ctx)

	// gRPC server
	grpcSrv := grpcsrv.NewServer()
	computev1.RegisterInstanceServiceServer(grpcSrv, handler.NewInstanceHandler(instanceSvc, hub))
	computev1.RegisterDiskServiceServer(grpcSrv, handler.NewDiskHandler(diskSvc, hub))
	computev1.RegisterImageServiceServer(grpcSrv, handler.NewImageHandler(imageSvc))
	computev1.RegisterSnapshotServiceServer(grpcSrv, handler.NewSnapshotHandler(snapshotSvc, hub))
	computev1.RegisterComputeInternalServiceServer(grpcSrv, handler.NewComputeInternalHandler(
		instanceSvc, diskSvc, snapshotSvc,
		instanceRepo, diskRepo, snapshotRepo,
	))

	listener, err := net.Listen("tcp", ":"+cfg.GrpcPort)
	if err != nil {
		return err
	}
	logger.Info("kacho-compute listening", "port", cfg.GrpcPort)

	go func() {
		<-ctx.Done()
		grpcSrv.GracefulStop()
	}()

	return grpcSrv.Serve(listener)
}

func runMigrate(cfg config.Config, direction string) {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("goose dialect: %v", err)
	}

	db, err := sql.Open("pgx", cfg.DSN())
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
