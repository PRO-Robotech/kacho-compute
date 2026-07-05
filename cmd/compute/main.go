// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
	"github.com/PRO-Robotech/kacho-corelib/outbox/metrics"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/check"
	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaboot"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/handler"
	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
	"github.com/PRO-Robotech/kacho-compute/internal/observability/health"
	computemetrics "github.com/PRO-Robotech/kacho-compute/internal/observability/metrics"
	"github.com/PRO-Robotech/kacho-compute/internal/operationresolver"
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

	projectClient, geoZones, closers, err := dialPeers(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	svcs := buildServices(pool, projectClient, geoZones, opsRepo)

	// Fail-closed boot-gate: when KACHO_COMPUTE_REQUIRE_IAM=true, mutating Create is
	// refused and readiness is NotReady until the register-drainer is IAM-connected.
	// Starts NOT connected; SetConnected(true) fires once the drainer dial succeeds.
	bootGate := bootgate.New(bootgate.Config{RequireIAM: cfg.RequireIAM, Service: "kacho-compute"})

	// Prometheus observability adapter: приватный реестр, питает outbox-recorder,
	// LRO-worker/reconciler recorder и diagnostic /metrics. Заменяет in-memory
	// MemRecorder (метрики не экспортировались) и NopRecorder LRO-worker'а.
	metricsAdapter := computemetrics.New(buildVersion, buildCommit)
	var outboxRec metrics.Recorder = metricsAdapter
	var lroRec operations.Recorder = metricsAdapter

	// background — фоновые loop'ы под супервизором (errgroup): неожиданный exit
	// флипает readiness в shutting-down и триггерит graceful-shutdown (не
	// fire-and-forget). Заполняется ниже (LRO-reconciler, register-drainer,
	// outbox-backstop), запускается в supervised errgroup перед Serve.
	type bgWorker struct {
		name string
		run  func(context.Context) error
	}
	var background []bgWorker

	// LRO worker (default-registry) поднимается ДО приёма трафика: ConfigureDefault
	// подключает Prometheus-Recorder (live terminal-write/inflight метрики — раньше
	// NopRecorder), Start делает Ready()=true без единой мутации (нет
	// readiness-deadlock «NotReady → нет Run → worker не стартует»).
	if err := startLROWorker(lroRec, logger); err != nil {
		return fmt.Errorf("start LRO worker: %w", err)
	}

	// Durable LRO recovery: доменный resolver + corelib-reconciler поверх schema
	// public. RecoverAll прогоняется ДО приёма трафика (осиротевшие операции
	// умершего worker'а — backlog-overflow, terminal-write retry exhausted,
	// shutdown, crash mid-op — разрешаются в терминал); периодический Run — backstop
	// под супервизором.
	lroReaders := operationresolver.Readers{
		Disk:     repo.NewDiskRepo(pool),
		Image:    repo.NewImageRepo(pool),
		Snapshot: repo.NewSnapshotRepo(pool),
		Instance: repo.NewInstanceRepo(pool),
	}
	lroReconciler := startLRORecovery(ctx, pool, lroReaders, lroRec, logger)
	background = append(background, bgWorker{"lro-reconciler", func(c context.Context) error {
		lroReconciler.Run(c)
		return nil
	}})

	// register-drainer — applies FGA owner-tuple register/unregister intents
	// (compute_fga_register_outbox, written transactionally by repo.Insert/Delete)
	// via kacho-iam InternalIAMService.RegisterResource/UnregisterResource over the
	// (optionally mTLS) compute→iam edge. Idempotent + retry-on-Unavailable; the
	// owner-tuple is never lost. Default-on; without it created resources get no
	// per-resource FGA tuple. Drainer Run-loop + outbox backstop (reconciler +
	// metrics collector) идут под супервизором, а не fire-and-forget.
	if cfg.FGARegisterDrainerEnabled {
		drainRun, drainCloser, derr := startRegisterDrainer(cfg, pool, outboxRec, logger)
		if derr != nil {
			return fmt.Errorf("start register-drainer: %w", derr)
		}
		defer drainCloser()
		background = append(background, bgWorker{"fga-register-drainer", drainRun})
		// Drainer dial established → IAM-register delivery path is up: open the
		// boot-gate + start the reconciler/metrics backstop.
		bootGate.SetConnected(true)
		reconRun, colRun, berr := startBackstop(ctx, pool, outboxRec, logger)
		if berr != nil {
			return fmt.Errorf("start outbox backstop: %w", berr)
		}
		background = append(background,
			bgWorker{"fga-register-reconciler", reconRun},
			bgWorker{"outbox-metrics-collector", colRun},
		)
	} else {
		logger.Warn("FGA register-drainer DISABLED (KACHO_COMPUTE_FGA_REGISTER_DRAINER_ENABLED=false) — " +
			"created resources will not get their per-resource FGA owner-tuple registered in IAM")
	}

	// principal — единственный subject per-RPC FGA Check. На ОБОИХ листенерах он
	// привязан к транспорту через trust-aware связку (anti-spoof, security.md
	// «authN+authZ на обоих listener'ах», «Internal = trusted — запрещённое
	// допущение»):
	//   1. UnaryCertIdentityExtract — извлекает module-identity SAN из
	//      verified mTLS client-cert'а и помечает peer'а verified/unverified;
	//      insecure dev-listener (mTLS off) → no-op (back-compat).
	//   2. UnaryTrustedPrincipalExtract(WithTrustedForwarders(<gateway-SAN>)) —
	//      читает x-kacho-principal-* metadata, но выставляет principal'а downstream
	//      (operations.PrincipalFromContext → subject Check'а + Operation.created_by)
	//      ТОЛЬКО когда peer mTLS-verified И (если allow-list задан) его SAN —
	//      доверенный форвардер (api-gateway). На недоверенном/не-форвардер peer'е
	//      forwarded principal снимается → SystemPrincipal → Check fail-closed.
	//      MUST идти ПОСЛЕ CertIdentityExtract. Пустой allow-list (default) →
	//      доверяем любому verified peer'у (паритет с kacho-iam internal).
	// Прежняя grpcsrv.UnaryPrincipalExtract доверяла x-kacho-principal-* metadata
	// любого peer'а безусловно (spoof: peer без cert'а форжил чужого principal'а).
	//
	// boot-gate: guardCreateUnary FIRST on the public chain — a
	// mutating tenant-resource Create is refused (UNAVAILABLE) when require-iam is
	// armed and the register-drainer is not IAM-connected, so no resource is created
	// without a deliverable owner-tuple intent. Read RPCs are untouched.
	forwarders := cfg.AuthZTrustedForwarderSANs
	publicUnary := []grpc.UnaryServerInterceptor{
		fgaboot.GuardCreateUnary(bootGate),
		grpcsrv.UnaryCertIdentityExtract(),
		grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		handler.TenantUnaryInterceptor(false, productionMode),
	}
	publicStream := []grpc.StreamServerInterceptor{
		grpcsrv.StreamCertIdentityExtract(),
		grpcsrv.StreamTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		handler.TenantStreamInterceptor(false, productionMode),
	}

	// Internal listener (:9091) — тот же authN+authZ, что и public (security-инвариант:
	// «authN+authZ на обоих listener'ах»; internal-периметр НЕ доверенный). Та же
	// trust-aware principal-связка: CertIdentityExtract → TrustedPrincipalExtract.
	// Catalog-admin internal RPC (InternalDiskTypeService mutations) relation-gated
	// (`system_admin on cluster:cluster_kacho_root`, internal/check/permission_map.go);
	// без verified cert'а / вне allow-list форвардеров их principal снимается →
	// Check fail-closed. InternalWatchService/Watch — <exempt> (нет в PermissionMap),
	// пропускается methodIsInternal; dev-mode (mTLS off) работает как раньше.
	internalUnary := []grpc.UnaryServerInterceptor{
		grpcsrv.UnaryCertIdentityExtract(),
		grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		handler.TenantUnaryInterceptor(true, productionMode),
	}
	internalStream := []grpc.StreamServerInterceptor{
		grpcsrv.StreamCertIdentityExtract(),
		grpcsrv.StreamTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		handler.TenantStreamInterceptor(true, productionMode),
	}

	var authzConn *grpc.ClientConn
	if cfg.AuthZIAMGRPCAddr != "" {
		// authz-conn → iam-internal:9091 — idle-prone (между всплесками authz Check
		// активных стримов нет) → idle=true: пинги держат conn тёплым.
		//
		// per-RPC InternalIAMService.Check + FGA-filtered List (этот conn
		// шарится с list-filter через buildListFilter) предъявляют client-cert mTLS
		// через cfg.IAMAuthzMTLS (enable=false → insecure dev; enable=true без
		// валидного cert-trio → startup error, fail-closed). Заменяет server-auth-only
		// cfg.AuthZIAMTLS bool, который не предъявлял cert (был бы отвергнут iam с
		// required client-cert).
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
	// Fail-closed (defense-in-depth): в production отсутствие authz-interceptor'а —
	// фатально (без per-RPC FGA Check подделанная x-kacho-* metadata даёт эскалацию,
	// а List отдаётся без list-filter). В dev — graceful Warn+continue. Зеркалит
	// kacho-vpc/cmd/vpc/authz_wiring.go.
	authzIntr, err = authzWiringDecision(productionMode, authzIntr, err)
	if err != nil {
		return fmt.Errorf("authz wiring: %w", err)
	}
	if authzIntr != nil {
		// Same interceptor instance on both listeners (shared Map + Cache).
		publicUnary = append(publicUnary, authzIntr.Unary())
		publicStream = append(publicStream, authzIntr.Stream())
		internalUnary = append(internalUnary, authzIntr.Unary())
		internalStream = append(internalStream, authzIntr.Stream())
		logger.Info("authz interceptor enabled",
			"iam_endpoint", cfg.AuthZIAMGRPCAddr,
			"breakglass", cfg.AuthZBreakglass,
			"listeners", "public+internal",
		)
	} else {
		// Dev — продолжаем без authz-interceptor'а (production-ветка уже отсеяна
		// authzWiringDecision выше как fatal).
		logger.Warn("authz interceptor NOT enabled — KACHO_COMPUTE_AUTHZ_IAM_GRPC_ADDR not configured (dev mode)")
	}

	// FGA-filtered List handlers. Build the filter from
	// configurable env vars (ListFilter*). If iam-authz conn is unavailable
	// (dev / breakglass), filter is nil → handler bypasses FGA filtering.
	listFilter := buildListFilter(cfg, authzConn, logger)

	// opt-in mTLS server-creds per listener (enable=false → insecure, dev
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
		grpc.ChainUnaryInterceptor(internalUnary...),
		grpc.ChainStreamInterceptor(internalStream...),
	)
	registerPublicServices(grpcSrv, svcs, opsRepo, listFilter)
	registerInternalServices(internalSrv, svcs, pool, cfg.MigrateDSN(), logger, cfg.WatchMaxStreams)

	// Dependency-aware readiness: /readyz отражает здоровье критичных зависимостей
	// (database / register-drainer / lro-worker / iam-authz), /healthz — только
	// живость процесса (защита от restart-storm). Результат зеркалится в
	// dependency_up Prometheus-gauge.
	healthAgg := health.New(
		buildReadinessCheckers(pool, bootGate, authzConn),
		health.WithResultObserver(metricsAdapter.SetDependencyUp),
	)
	// Diagnostic HTTP-listener (cluster-internal): /metrics + /healthz + /readyz.
	// Пустой KACHO_COMPUTE_METRICS_ADDR → не поднимается (back-compat).
	diagTask, diagShutdown, err := startDiagnosticListener(cfg.MetricsAddr, metricsAdapter, healthAgg, logger)
	if err != nil {
		return fmt.Errorf("start diagnostic listener: %w", err)
	}

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

	// Единый shutdown-триггер (sync.Once): флипает readiness в shutting_down (kubelet
	// перестаёт слать трафик ДО GracefulStop), отменяет ctx (фоновые loop'ы выходят),
	// гасит оба gRPC-сервера. Вызывается из shutdown-waiter (SIGTERM), из краша
	// любого supervised-task'а и из superviseBackground при неожиданном exit'е.
	var shutdownOnce sync.Once
	shutdownCh := make(chan struct{})
	triggerShutdown := func() {
		shutdownOnce.Do(func() {
			healthAgg.SetShuttingDown()
			close(shutdownCh)
			cancel()
			internalSrv.GracefulStop()
			grpcSrv.GracefulStop()
		})
	}

	var g errgroup.Group
	// Фоновые loop'ы под супервизором: неожиданный exit (ctx ещё жив) флипает
	// readiness и триггерит shutdown; штатный возврат после ctx-cancel → nil.
	for _, bg := range background {
		g.Go(func() error {
			return superviseBackground(ctx, bg.name, bg.run, triggerShutdown, logger)
		})
	}
	// Diagnostic HTTP-listener (когда поднят).
	if diagTask != nil {
		g.Go(func() error {
			if derr := diagTask(); derr != nil {
				logger.Error("diagnostic listener stopped", "err", derr)
				triggerShutdown()
				return fmt.Errorf("diagnostic listener: %w", derr)
			}
			return nil
		})
	}
	// internal gRPC server.
	g.Go(func() error {
		if serr := internalSrv.Serve(internalListener); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			logger.Error("internal grpc server stopped", "err", serr)
			triggerShutdown()
			return fmt.Errorf("internal grpc server: %w", serr)
		}
		return nil
	})
	// public gRPC server.
	g.Go(func() error {
		if serr := grpcSrv.Serve(listener); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			triggerShutdown()
			return fmt.Errorf("public grpc server: %w", serr)
		}
		return nil
	})
	// shutdown-waiter: SIGTERM/SIGINT (ctx) ИЛИ краш любого task'а (shutdownCh) →
	// triggerShutdown → дрейн LRO worker'ов → гашение diagnostic-listener'а последним
	// (probe-flip /readyz→503 успевает отработать до закрытия порта).
	g.Go(func() error {
		select {
		case <-ctx.Done():
		case <-shutdownCh:
		}
		triggerShutdown()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer drainCancel()
		if werr := operations.Wait(drainCtx); werr != nil {
			logger.Warn("operations workers did not finish in time", "err", werr, "active", operations.Active())
		}
		diagShutdown(drainCtx)
		return nil
	})

	return g.Wait()
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
		// TLS-check on the IAM peer (project-existence-check edge).
		if !cfg.IAMTLS {
			return false, fmt.Errorf("production-strict mode: KACHO_COMPUTE_IAM_TLS=true required")
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

// dialPeers открывает gRPC-клиенты к peer-сервисам (kacho-iam — public :9090 для
// project-existence-check; kacho-geo — public :9090 для zone_id-валидации Instance)
// либо возвращает no-op-заглушки при KACHO_COMPUTE_SKIP_PEER_VALIDATION=true.
//
// project-existence-check идёт в kacho-iam.ProjectService.Get.
//
// zone_id-валидация Instance идёт через geo.v1.ZoneService.Get (clients.GeoClient);
// Geography (Region/Zone) принадлежит kacho-geo — compute их больше не обслуживает,
// а лишь валидирует свой zone_id как consumer.
func dialPeers(cfg config.Config, logger *slog.Logger) (service.ProjectClient, service.ZoneRegistry, []*grpc.ClientConn, error) {
	if cfg.SkipPeerValidation {
		logger.Warn("KACHO_COMPUTE_SKIP_PEER_VALIDATION=true — cross-service existence-check disabled (dev/test only)")
		return clients.NoopProjectClient{}, clients.NoopGeoClient{}, nil, nil
	}
	// iam (public ProjectService.Get) — активно используется на request-path каждой
	// мутации → idle=false (трафик есть, idle-пинги не нужны; keepalive всё равно
	// ставится для half-open-detection при паузах).
	//
	// compute→iam ProjectService.Get (:9090) предъявляет client-cert mTLS
	// через cfg.IAMProjectMTLS (enable=false → insecure dev; enable=true без
	// валидного cert-trio → startup error, fail-closed). Заменяет server-auth-only
	// cfg.IAMTLS bool, который не предъявлял cert (был бы отвергнут iam с
	// required client-cert).
	iamCreds, err := grpcclient.TLSClientTransportCreds(cfg.IAMProjectMTLS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("compute→iam ProjectService.Get mTLS creds: %w", err)
	}
	iamConn, err := dialPeerCreds(cfg.IAMGRPCAddr, iamCreds, false)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial iam: %w", err)
	}
	// compute→geo zone_id-валидация Instance (geo.v1.ZoneService.Get,
	// public :9090). Per-edge client-cert mTLS через cfg.GeoMTLS (enable=false →
	// insecure dev; enable=true без валидного cert-trio → startup error,
	// fail-closed) — паритет с compute→iam ребром.
	geoCreds, err := grpcclient.TLSClientTransportCreds(cfg.GeoMTLS)
	if err != nil {
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("compute→geo ZoneService.Get mTLS creds: %w", err)
	}
	geoConn, err := dialPeerCreds(cfg.GeoGRPCAddr, geoCreds, false)
	if err != nil {
		_ = iamConn.Close()
		return nil, nil, nil, fmt.Errorf("dial geo: %w", err)
	}
	logger.Info("compute→geo resource-validation mTLS state",
		"geo_zone_validate_mtls", cfg.GeoMTLS.Enable,
	)
	return clients.NewProjectClient(iamConn), clients.NewGeoClient(geoConn),
		[]*grpc.ClientConn{iamConn, geoConn}, nil
}

// peerKeepalive — keepalive-параметры для peer-conn. idle=true для
// преимущественно-idle conn'ов (authz → iam-internal): PermitWithoutStream держит
// conn тёплым пингами без активных стримов, прямо лечит half-open-столл.
func peerKeepalive(idle bool) keepalive.ClientParameters {
	return grpcclient.KeepaliveParams(idle)
}

// peerDialOptsCreds — seam-функция (тестируемая): собирает []grpc.DialOption из
// готовых transport-creds + keepalive по idle. Единая точка для обоих путей:
// legacy bool-peer (peerDialOpts) и per-edge client-cert mTLS iam-рёбер,
// которые резолвят creds через corelib grpcclient.TLSClientTransportCreds. grpc.NewClient
// не отдаёт опции назад — тест инспектирует именно этот набор.
func peerDialOptsCreds(creds credentials.TransportCredentials, idle bool) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpcclient.KeepaliveDialOption(idle),
	}
}

// peerDialOpts — server-auth-only seam (bool useTLS), делегирует в
// peerDialOptsCreds. Сохраняется как тестируемый seam bool-контракта keepalive
// (dialpeer_test.go); live-рёбра (iam/geo) идут через client-cert mTLS-путь
// peerDialOptsCreds/dialPeerCreds.
func peerDialOpts(useTLS, idle bool) []grpc.DialOption {
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		creds = insecure.NewCredentials()
	}
	return peerDialOptsCreds(creds, idle)
}

// dialPeerCreds открывает gRPC-conn к peer-сервису, предъявляя готовые transport-
// creds (per-edge client-cert mTLS). Используется для compute→iam read/authz
// рёбер: creds резолвятся из cfg.IAMProjectMTLS / cfg.IAMAuthzMTLS через corelib
// grpcclient (enable=false → insecure, dev backward-compat).
func dialPeerCreds(addr string, creds credentials.TransportCredentials, idle bool) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, peerDialOptsCreds(creds, idle)...)
}

// buildServices создаёт все repo'ы поверх pool и собирает из них бизнес-сервисы.
//
// existence-check zone_id (Instance/Disk Create, Disk Relocate) идёт в kacho-geo
// через geoZones (service.ZoneRegistry, реализован clients.GeoClient). compute
// больше НЕ обслуживает Region/Zone — Geography (Region/Zone) принадлежит
// kacho-geo; локальные таблицы `zones`/`regions` сняты миграцией
// 0011_drop_geography. Режим KACHO_COMPUTE_SKIP_PEER_VALIDATION учтён на уровне
// dialPeers (NoopProjectClient/NoopGeoClient — любой project/зона «существует»).
func buildServices(pool *pgxpool.Pool, projectClient service.ProjectClient, geoZones service.ZoneRegistry, opsRepo operations.Repo) *services {
	diskRepo := repo.NewDiskRepo(pool)
	imageRepo := repo.NewImageRepo(pool)
	snapshotRepo := repo.NewSnapshotRepo(pool)
	instanceRepo := repo.NewInstanceRepo(pool)
	diskTypeRepo := repo.NewDiskTypeRepo(pool)

	diskTypeSvc := service.NewDiskTypeService(diskTypeRepo)
	return &services{
		disk:     service.NewDiskService(diskRepo, imageRepo, snapshotRepo, diskTypeRepo, geoZones, projectClient, opsRepo),
		image:    service.NewImageService(imageRepo, diskRepo, snapshotRepo, projectClient, opsRepo),
		snapshot: service.NewSnapshotService(snapshotRepo, diskRepo, projectClient, opsRepo),
		diskType: diskTypeSvc,
		instance: service.NewInstanceService(instanceRepo, diskRepo, imageRepo, snapshotRepo, geoZones, projectClient, opsRepo),
	}
}

// registerPublicServices — публичные RPC + OperationService на внешний listener
// (:9090, проксируется api-gateway).
//
// List handlers получают listFilter (FGA filter); может быть nil — тогда
// FGA-фильтрация на List отключена (dev/breakglass). Catalog (DiskType) — public
// read, FGA bypass not needed (handler skips). Region/Zone serving снят —
// Geography принадлежит kacho-geo.
func registerPublicServices(srv *grpc.Server, svcs *services, opsRepo operations.Repo, listFilter authzfilter.Filter) {
	computev1.RegisterDiskServiceServer(srv, handler.NewDiskHandler(svcs.disk, listFilter))
	computev1.RegisterImageServiceServer(srv, handler.NewImageHandler(svcs.image, listFilter))
	computev1.RegisterSnapshotServiceServer(srv, handler.NewSnapshotHandler(svcs.snapshot, listFilter))
	computev1.RegisterDiskTypeServiceServer(srv, handler.NewDiskTypeHandler(svcs.diskType))
	computev1.RegisterInstanceServiceServer(srv, handler.NewInstanceHandler(svcs.instance, listFilter))
	operationpb.RegisterOperationServiceServer(srv, handler.NewOperationHandler(opsRepo))
}

// buildListFilter возвращает authzfilter.Filter, готовый к подвешиванию в public
// List handlers. Если KACHO_COMPUTE_LIST_FILTER_ENABLED=false
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

// startRegisterDrainer dials the kacho-iam internal endpoint over the
// compute→iam edge (mTLS opt-in via cfg.IAMRegisterClientCreds — enable=false →
// insecure dev) and starts a corelib outbox/drainer over
// compute_fga_register_outbox. Each pending intent is replayed through
// InternalIAMService.RegisterResource / UnregisterResource by the applier
// (idempotent; Unavailable → retry with backoff; InvalidArgument → poison). The
// drainer Run-loop owns claim-CAS + advisory-lock for exactly-once across replicas
// (corelib W1.1). Returns a closer that shuts the dial-conn; nil error on success.
//
// The drainer dials the iam-internal :9091 listener — RegisterResource is an
// Internal-only RPC; the addr is derived from AuthZIAMGRPCAddr (the
// existing iam-internal endpoint compute already uses for Check) and falls back to
// IAMGRPCAddr when unset.
//
// Возвращает run-функцию drainer'а (d.Run, вешается на супервизор в runServe — не
// fire-and-forget) и closer dial-conn'а; conn живёт всё время работы run и
// закрывается defer'ом в runServe после g.Wait.
func startRegisterDrainer(cfg config.Config, pool *pgxpool.Pool, rec metrics.Recorder, logger *slog.Logger) (run func(context.Context) error, closer func(), err error) {
	addr := cfg.AuthZIAMGRPCAddr
	if addr == "" {
		addr = cfg.IAMGRPCAddr
	}

	creds, cerr := cfg.IAMRegisterClientCreds()
	if cerr != nil {
		return nil, nil, fmt.Errorf("compute→iam register mTLS creds: %w", cerr)
	}
	// idle-prone edge (register-drainer is mostly waiting on NOTIFY) → keepalive
	// idle pings keep the conn warm.
	conn, cerr := grpc.NewClient(addr, creds, grpcclient.KeepaliveDialOption(true))
	if cerr != nil {
		return nil, nil, fmt.Errorf("dial kacho-iam (register-drainer): %w", cerr)
	}

	applier := clients.NewIAMRegisterApplier(conn)
	d, derr := drainer.New[fgaintent.Payload](
		pool,
		drainer.Config{
			Table:   computeFGAOutboxTable,
			Channel: computeFGAOutboxChannel,
		},
		func(b []byte) (fgaintent.Payload, error) {
			p, decErr := fgaintent.Decode(b)
			if decErr != nil {
				// Malformed payload — permanent poison, never retried.
				return fgaintent.Payload{}, errors.Join(drainer.ErrPermanent, decErr)
			}
			return p, nil
		},
		applier.Apply,
		logger.With("component", "fga-register-drainer"),
		// Each poisoned row bumps outbox_poisoned_total{table=…}.
		drainer.WithPoisonObserver[fgaintent.Payload](func() {
			rec.IncPoisoned(computeFGAOutboxTable)
		}),
	)
	if derr != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("build register-drainer: %w", derr)
	}

	logger.Info("FGA register-drainer started",
		"iam_addr", addr, "mtls", cfg.IAMRegisterMTLS.Enable)

	return d.Run, func() { _ = conn.Close() }, nil
}

// registerInternalServices — kacho-only/admin RPC на internal listener (:9091,
// не маршрутизируется наружу; NetworkPolicy + requireAdmin-interceptor).
func registerInternalServices(srv *grpc.Server, svcs *services, pool *pgxpool.Pool, dsn string, logger *slog.Logger, watchMaxStreams int) {
	computev1.RegisterInternalWatchServiceServer(srv, handler.NewInternalWatchHandler(pool, dsn, logger.With("component", "internal-watch"), watchMaxStreams))
	computev1.RegisterInternalDiskTypeServiceServer(srv, handler.NewInternalDiskTypeHandler(svcs.diskType))
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
