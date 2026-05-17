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

	// update.
	got.Name = "disk-b"
	got.Size = 8 << 20
	updated, err := r.Update(ctx, got)
	require.NoError(t, err)
	assert.Equal(t, "disk-b", updated.Name)
	assert.Equal(t, int64(8<<20), updated.Size)

	// list.
	list, _, err := r.List(ctx, service.DiskFilter{ProjectID: "f1"}, service.Pagination{})
	require.NoError(t, err)
	assert.Len(t, list, 1)

	// not attached.
	attached, err := r.IsAttached(ctx, d.ID)
	require.NoError(t, err)
	assert.False(t, attached)

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

func TestIntegration_InstanceRepo_AttachFKCascade(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)
	instRepo := repo.NewInstanceRepo(pool)

	bootDiskID := ids.NewID(ids.PrefixDisk)
	dataDiskID := ids.NewID(ids.PrefixDisk)
	_, err = diskRepo.Insert(ctx, &domain.Disk{ID: dataDiskID, ProjectID: "f", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady})
	require.NoError(t, err)

	inID := ids.NewID(ids.PrefixInstance)
	in := &domain.Instance{
		ID: inID, ProjectID: "f", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "vm-1",
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
		NetworkInterfaces: []domain.NetworkInterface{{Index: "0", SubnetID: "e9bsub", PrimaryV4Address: "10.0.0.10"}},
		AttachedDisks:     []domain.AttachedDisk{{DiskID: bootDiskID, IsBoot: true, AutoDelete: true}},
	}
	inlineBoot := &domain.Disk{ID: bootDiskID, ProjectID: "f", CreatedAt: time.Now().UTC().Truncate(time.Microsecond), ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady}
	created, err := instRepo.Insert(ctx, in, []*domain.Disk{inlineBoot})
	require.NoError(t, err)
	require.Len(t, created.AttachedDisks, 1)
	require.Len(t, created.NetworkInterfaces, 1)

	// boot disk attached → cannot delete (FK RESTRICT).
	err = diskRepo.Delete(ctx, bootDiskID)
	require.ErrorIs(t, err, service.ErrFailedPrecondition)

	// attach data disk.
	updated, err := instRepo.AttachDisk(ctx, inID, domain.AttachedDisk{DiskID: dataDiskID, DeviceName: "data0"})
	require.NoError(t, err)
	require.Len(t, updated.AttachedDisks, 2)
	attached, err := diskRepo.IsAttached(ctx, dataDiskID)
	require.NoError(t, err)
	require.True(t, attached)

	// detach data disk.
	updated, err = instRepo.DetachDisk(ctx, inID, dataDiskID)
	require.NoError(t, err)
	require.Len(t, updated.AttachedDisks, 1)

	// SetStatusCAS: RUNNING → STOPPED.
	updated, err = instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.NoError(t, err)
	require.Equal(t, domain.InstanceStatusStopped, updated.Status)
	// CAS fail: instance уже STOPPED, повторный CAS RUNNING→STOPPED → FailedPrecondition.
	_, err = instRepo.SetStatusCAS(ctx, inID, domain.InstanceStatusRunning, domain.InstanceStatusStopped)
	require.ErrorIs(t, err, service.ErrFailedPrecondition)

	// Delete instance with auto-delete boot disk → NIC + attached_disks cleaned via CASCADE, boot disk deleted.
	require.NoError(t, instRepo.Delete(ctx, inID, []string{bootDiskID}))
	_, err = instRepo.Get(ctx, inID)
	require.ErrorIs(t, err, service.ErrNotFound)
	_, err = diskRepo.Get(ctx, bootDiskID)
	require.ErrorIs(t, err, service.ErrNotFound)
	// data disk (auto_delete=false on attach) survives.
	_, err = diskRepo.Get(ctx, dataDiskID)
	require.NoError(t, err)
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

	dtr := repo.NewDiskTypeRepo(pool)
	list, _, err := dtr.List(ctx, service.Pagination{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list), 4) // seeded
	ssd, err := dtr.Get(ctx, "network-ssd")
	require.NoError(t, err)
	require.NotEmpty(t, ssd.ZoneIDs)

	zr := repo.NewZoneRepo(pool)
	zones, _, err := zr.List(ctx, service.Pagination{})
	require.NoError(t, err)
	require.Len(t, zones, 3)
	z, err := zr.Get(ctx, "ru-central1-a")
	require.NoError(t, err)
	require.Equal(t, domain.ZoneStatusUp, z.Status)

	// admin CRUD.
	created, err := zr.Insert(ctx, &domain.Zone{ID: "ru-central1-x", RegionID: "ru-central1", Status: domain.ZoneStatusUp})
	require.NoError(t, err)
	require.Equal(t, "ru-central1-x", created.ID)
	require.NoError(t, zr.Delete(ctx, "ru-central1-x"))
}
