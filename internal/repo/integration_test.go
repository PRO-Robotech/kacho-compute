// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
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

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(db, "."))
	return dsn
}

func TestIntegration_DiskRepo_CRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	r := repo.NewDiskRepo(pool)
	d := &domain.Disk{
		ID: ids.NewID(ids.PrefixDisk), ProjectID: "f1", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Name: "disk-a", TypeID: "network-ssd", ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096,
		Status: domain.DiskStatusReady, Labels: map[string]string{"env": "test"},
	}
	created, err := r.Insert(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, "disk-a", created.Name)
	assert.Equal(t, domain.DiskStatusReady, created.Status)

	got, err := r.Get(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, "test", got.Labels["env"])

	// duplicate name → AlreadyExists.
	dup := *d
	dup.ID = ids.NewID(ids.PrefixDisk)
	_, err = r.Insert(ctx, &dup)
	require.ErrorIs(t, err, service.ErrAlreadyExists)

	// update (name/size only, no labels → emitLabelsRegister=false).
	got.Name = "disk-b"
	got.Size = 8 << 20
	updated, err := r.Update(ctx, got, false, []string{"name", "size"})
	require.NoError(t, err)
	assert.Equal(t, "disk-b", updated.Name)
	assert.Equal(t, int64(8<<20), updated.Size)

	// list.
	list, _, err := r.List(ctx, service.DiskFilter{ProjectID: "f1"}, service.Pagination{})
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// delete.
	require.NoError(t, r.Delete(ctx, d.ID))
	_, err = r.Get(ctx, d.ID)
	require.ErrorIs(t, err, service.ErrNotFound)
}

func TestIntegration_ImageAndSnapshotRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	ir := repo.NewImageRepo(pool)
	old := &domain.Image{ID: ids.NewID(ids.PrefixImage), ProjectID: "f", CreatedAt: time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond), Name: "img-old", Family: "ubuntu", Status: domain.ImageStatusReady, OsType: domain.OsTypeLinux}
	newer := &domain.Image{ID: ids.NewID(ids.PrefixImage), ProjectID: "f", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "img-new", Family: "ubuntu", Status: domain.ImageStatusReady, OsType: domain.OsTypeLinux}
	_, err = ir.Insert(ctx, old)
	require.NoError(t, err)
	_, err = ir.Insert(ctx, newer)
	require.NoError(t, err)
	latest, err := ir.GetLatestByFamily(ctx, "f", "ubuntu")
	require.NoError(t, err)
	assert.Equal(t, newer.ID, latest.ID)

	sr := repo.NewSnapshotRepo(pool)
	s := &domain.Snapshot{ID: ids.NewID(ids.PrefixSnapshot), ProjectID: "f", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "snap-1", SourceDiskID: "epdd", DiskSize: 4194304, StorageSize: 4194304, Status: domain.SnapshotStatusReady}
	created, err := sr.Insert(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, "snap-1", created.Name)
	require.NoError(t, sr.Delete(ctx, s.ID))
}

// TestIntegration_InstanceRepo_Lifecycle покрывает post-cutover repo-поверхность
// InstanceRepo: Insert (без привязок — attached_disks удалена), GateForAttach
// state-CAS, SetStatusCAS, MarkDeleting, Delete (финальный row-delete). Attach-state
// живёт в kacho-storage — здесь его нет (см. storage S2 integration).
func TestIntegration_InstanceRepo_Lifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	instRepo := repo.NewInstanceRepo(pool)

	inID := ids.NewID(ids.PrefixInstance)
	in := &domain.Instance{
		ID: inID, ProjectID: "f", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "vm-1",
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
	}
	created, err := instRepo.Insert(ctx, in)
	require.NoError(t, err)
	require.Empty(t, created.AttachedDisks)

	// GateForAttach: RUNNING → возвращает self-describing payload.
	zone, project, name, err := instRepo.GateForAttach(ctx, inID)
	require.NoError(t, err)
	require.Equal(t, "ru-central1-a", zone)
	require.Equal(t, "f", project)
	require.Equal(t, "vm-1", name)

	// SetStatusCAS: RUNNING → STOPPED; повтор → FailedPrecondition.
	updated, err := instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusStopped, updated.Status)
	_, err = instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.ErrorIs(t, err, service.ErrFailedPrecondition)

	// GateForAttach всё ещё проходит (STOPPED ∈ {RUNNING, STOPPED}).
	_, _, _, err = instRepo.GateForAttach(ctx, inID)
	require.NoError(t, err)

	// MarkDeleting → DELETING; GateForAttach теперь падает (attach-vs-delete гейт).
	di, err := instRepo.MarkDeleting(ctx, inID)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusDeleting, di.Status)
	_, _, _, err = instRepo.GateForAttach(ctx, inID)
	require.ErrorIs(t, err, service.ErrFailedPrecondition)

	// Delete (финальный row-delete) → NotFound на повторном Get.
	require.NoError(t, instRepo.Delete(ctx, inID))
	_, err = instRepo.Get(ctx, inID)
	require.ErrorIs(t, err, service.ErrNotFound)
	// GateForAttach на удалённом → NotFound.
	_, _, _, err = instRepo.GateForAttach(ctx, inID)
	require.ErrorIs(t, err, service.ErrNotFound)
}

func TestIntegration_CatalogRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// Region/Zone serving (ZoneRepo/RegionRepo) removed — Geography is
	// owned by kacho-geo; the local zones/regions tables are dropped by migration
	// 0011_drop_geography (see TestIntegration_DropGeographyMigration). DiskType
	// stays compute-owned.
	dtr := repo.NewDiskTypeRepo(pool)
	list, _, err := dtr.List(ctx, service.Pagination{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list), 4) // seeded
	ssd, err := dtr.Get(ctx, "network-ssd")
	require.NoError(t, err)
	require.NotEmpty(t, ssd.ZoneIDs)
}
