package repo_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
)

// TestIntegration_DropGeographyMigration verifies the S7 drop-migration
// (0011_drop_geography): Geography (Region/Zone) is now owned by kacho-geo, so
// compute's local `zones`/`regions` tables are dropped. DiskType stays (it shares
// the catalog schema). Down recreates regions+zones (FK zones.region_id→regions
// ON DELETE RESTRICT) + reseeds ru-central1{,-a,-b,-d}.
func TestIntegration_DropGeographyMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
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

	// Up to head — drop-geography must have run: zones/regions gone, disk_types kept.
	require.NoError(t, goose.Up(db, "."))
	require.False(t, tableExists(t, db, "zones"), "zones table must be dropped at head")
	require.False(t, tableExists(t, db, "regions"), "regions table must be dropped at head")
	require.True(t, tableExists(t, db, "disk_types"), "disk_types must survive geography drop")
	require.True(t, tableExists(t, db, "instances"), "instances must survive geography drop")

	// Down to version 10 — reverts every migration after 0010 (which includes
	// 0011_drop_geography), so 0011's Down recreates regions + zones with seed +
	// FK. Targeting version 10 explicitly (not a single Down step) keeps this
	// assertion stable as later additive migrations land on top of 0011.
	require.NoError(t, goose.DownTo(db, ".", 10))
	require.True(t, tableExists(t, db, "zones"), "Down must recreate zones")
	require.True(t, tableExists(t, db, "regions"), "Down must recreate regions")
	require.Equal(t, 3, rowCount(t, db, "zones"), "Down must reseed 3 zones")
	require.Equal(t, 1, rowCount(t, db, "regions"), "Down must reseed ru-central1")

	// FK zones.region_id → regions ON DELETE RESTRICT: a region with zones cannot be deleted.
	_, err = db.ExecContext(ctx, `DELETE FROM regions WHERE id = 'ru-central1'`)
	require.Error(t, err, "FK RESTRICT must block deleting a region that still has zones")
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name=$1`, name,
	).Scan(&n))
	return n > 0
}

func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
	return n
}
