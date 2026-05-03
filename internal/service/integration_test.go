package service_test

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-compute/internal/config"
	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
	"github.com/PRO-Robotech/kacho-compute/internal/reconciler"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

func setupTestDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	pgc, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("kacho_compute_test"),
		postgres.WithUsername("compute"),
		postgres.WithPassword("compute"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	// Запускаем миграции.
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(db, "."))

	return dsn
}

func TestIntegration_DiskLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	dsn := setupTestDB(t)

	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	opsRepo := operations.NewRepo(pool, "public")
	diskRepo := repo.NewDiskRepo(pool)
	imageRepo := repo.NewImageRepo(pool)

	diskSvc := svc.NewDiskService(
		diskRepo,
		imageRepo,
		newMockFolderClient("folder-1"),
		opsRepo,
	)

	// Create disk.
	op, err := diskSvc.Create(ctx, svc.CreateDiskReq{
		FolderID:   "folder-1",
		Name:       "test-disk",
		DiskTypeID: "network-ssd",
		ZoneID:     "kacho-zone-a",
		Size:       "10Gi",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
	assert.False(t, op.Done)

	// Ждём пока goroutine выполнит doCreate (вставит диск в БД).
	time.Sleep(200 * time.Millisecond)

	// Найдём disk через список (folder-scoped list не работает без wait — нужен ID из operation metadata).
	disks, _, err := diskSvc.List(ctx, svc.DiskFilter{FolderID: "folder-1"}, svc.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, disks, 1)
	disk := disks[0]

	assert.Equal(t, "test-disk", disk.Name)
	assert.Equal(t, domain.DiskStatusCreating, disk.Status)
	assert.Equal(t, "folder-1", disk.FolderID)
}

func TestIntegration_SnapshotLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	dsn := setupTestDB(t)

	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	opsRepo := operations.NewRepo(pool, "public")
	diskRepo := repo.NewDiskRepo(pool)
	snapRepo := repo.NewSnapshotRepo(pool)

	// Вставляем диск напрямую для теста снимков.
	diskID := "a0000000-0000-0000-0000-000000000001"
	testDisk := &domain.Disk{
		ID:              diskID,
		FolderID:        "folder-1",
		Name:            "snap-source",
		Size:            "10Gi",
		Status:          domain.DiskStatusReady,
		Generation:      1,
		ResourceVersion: "rv1",
		DiskTypeID:      "network-ssd",
		ZoneID:          "kacho-zone-a",
		CreatedAt:       time.Now().UTC(),
		StatusLastTransitionAt: time.Now().UTC(),
	}
	_, err = diskRepo.Insert(ctx, testDisk)
	require.NoError(t, err)

	snapSvc := svc.NewSnapshotService(snapRepo, diskRepo, opsRepo)

	op, err := snapSvc.Create(ctx, svc.CreateSnapshotReq{
		FolderID: "folder-1",
		DiskID:   diskID,
		Name:     "my-snapshot",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)

	time.Sleep(200 * time.Millisecond)

	snaps, _, err := snapSvc.List(ctx, svc.SnapshotFilter{FolderID: "folder-1"}, svc.Pagination{PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, snaps, 1)
	assert.Equal(t, "my-snapshot", snaps[0].Name)
	assert.Equal(t, domain.SnapshotStatusCreating, snaps[0].Status)
}

func TestIntegration_ReconcilerDiskCreating(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := setupTestDB(t)

	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	opsRepo := operations.NewRepo(pool, "public")
	diskRepo := repo.NewDiskRepo(pool)
	snapRepo := repo.NewSnapshotRepo(pool)
	instRepo := repo.NewInstanceRepo(pool)

	simCfg := config.SimConfig{
		DiskCreateMinMS: 100,
		DiskCreateMaxMS: 200,
		ProvisionMinMS:  100,
		ProvisionMaxMS:  200,
		StartStopMinMS:  100,
		StartStopMaxMS:  200,
	}

	dispatcher := reconciler.NewDispatcher(
		instRepo,
		diskRepo,
		snapRepo,
		opsRepo,
		simCfg,
		slog.Default(),
	)

	reconcileDiskID := "b0000000-0000-0000-0000-000000000001"
	// Вставляем диск в статусе CREATING.
	testDisk := &domain.Disk{
		ID:              reconcileDiskID,
		FolderID:        "folder-1",
		Name:            "reconcile-disk",
		Size:            "10Gi",
		Status:          domain.DiskStatusCreating,
		Generation:      1,
		ResourceVersion: "rv1",
		DiskTypeID:      "network-ssd",
		ZoneID:          "kacho-zone-a",
		CreatedAt:       time.Now().UTC(),
		StatusLastTransitionAt: time.Now().UTC(),
	}
	_, err = diskRepo.Insert(ctx, testDisk)
	require.NoError(t, err)

	go dispatcher.Run(ctx)

	// Ждём перехода в READY.
	require.Eventually(t, func() bool {
		d, err := diskRepo.Get(ctx, reconcileDiskID)
		if err != nil {
			return false
		}
		return d.Status == domain.DiskStatusReady
	}, 10*time.Second, 200*time.Millisecond, "disk should become READY")

	d, err := diskRepo.Get(ctx, reconcileDiskID)
	require.NoError(t, err)
	assert.Equal(t, domain.DiskStatusReady, d.Status)
	assert.Equal(t, d.Generation, d.ObservedGeneration)
}
