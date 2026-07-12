// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/repo"
)

// fgaRegisterRow is a decoded compute_fga_register_outbox row used by the
// transactional-outbox assertions (intent written IN THE SAME writer-tx as the
// resource Insert/Delete).
type fgaRegisterRow struct {
	eventType    string
	resourceKind string
	resourceID   string
	payload      []byte
	sentAt       *time.Time
}

// queryFGARegisterRows returns every compute_fga_register_outbox row for a given
// resource_id (asserted to be in the writer-tx after Commit).
func queryFGARegisterRows(ctx context.Context, t *testing.T, pool interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, resourceID string) []fgaRegisterRow {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT event_type, resource_kind, resource_id, payload, sent_at
		   FROM compute_fga_register_outbox WHERE resource_id = $1 ORDER BY id`, resourceID)
	require.NoError(t, err)
	defer rows.Close()
	var out []fgaRegisterRow
	for rows.Next() {
		var r fgaRegisterRow
		require.NoError(t, rows.Scan(&r.eventType, &r.resourceKind, &r.resourceID, &r.payload, &r.sentAt))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func payloadTuples(t *testing.T, b []byte) []fgaintent.Tuple {
	t.Helper()
	var p fgaintent.Payload
	require.NoError(t, json.Unmarshal(b, &p))
	return p.Tuples
}

// TestInstance_SEC_D_04_RegisterIntentInWriterTx — Instance.Create writes
// exactly one fga.register intent in the SAME writer-tx as the Insert,
// payload carries project:<id> #project @compute_instance:<id>; sent_at IS NULL
// (register-drainer not run here).
func TestInstance_SEC_D_04_RegisterIntentInWriterTx(t *testing.T) {
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
	projectID := "proj-aaaaaaaaaaaaaaaaa"
	in := &domain.Instance{
		ID: inID, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "vm-a",
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
	}
	_, err = instRepo.Insert(ctx, in)
	require.NoError(t, err)

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	require.Len(t, rows, 1, "exactly one fga.register intent for the instance")
	assert.Equal(t, fgaintent.EventRegister, rows[0].eventType)
	assert.Equal(t, "Instance", rows[0].resourceKind)
	assert.Nil(t, rows[0].sentAt, "intent not yet applied (sent_at IS NULL)")

	tuples := payloadTuples(t, rows[0].payload)
	require.Len(t, tuples, 1)
	assert.Equal(t, "project:"+projectID, tuples[0].SubjectID)
	assert.Equal(t, "project", tuples[0].Relation)
	assert.Equal(t, "compute_instance:"+inID, tuples[0].Object)

	// And the domain outbox CREATED row is in the same committed state.
	var domainCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM compute_outbox WHERE resource_id = $1 AND event_type = 'CREATED'`, inID).Scan(&domainCount))
	assert.Equal(t, 1, domainCount)
}

// TestInstance_SEC_D_03_UnregisterIntentOnDelete — Instance.Delete writes an
// fga.unregister intent in the same writer-tx; payload
// carries the project-hierarchy tuple; the instance row is gone.
func TestInstance_SEC_D_03_UnregisterIntentOnDelete(t *testing.T) {
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
	projectID := "proj-bbbbbbbbbbbbbbbbb"
	in := &domain.Instance{
		ID: inID, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "vm-del",
		ZoneID: "ru-central1-a", PlatformID: "standard-v3", Cores: 2, Memory: 2 << 30, CoreFraction: 100,
		Status: domain.InstanceStatusRunning, FQDN: inID + ".auto.internal", NetworkSettingsType: "STANDARD",
	}
	_, err = instRepo.Insert(ctx, in)
	require.NoError(t, err)

	require.NoError(t, instRepo.Delete(ctx, inID))

	rows := queryFGARegisterRows(ctx, t, pool, inID)
	// register (from Create) + unregister (from Delete).
	var unreg *fgaRegisterRow
	for i := range rows {
		if rows[i].eventType == fgaintent.EventUnregister {
			unreg = &rows[i]
		}
	}
	require.NotNil(t, unreg, "Delete writes an fga.unregister intent")
	assert.Nil(t, unreg.sentAt)
	tuples := payloadTuples(t, unreg.payload)
	require.Len(t, tuples, 1)
	assert.Equal(t, "compute_instance:"+inID, tuples[0].Object)
	assert.Equal(t, "project:"+projectID, tuples[0].SubjectID)

	_, gerr := instRepo.Get(ctx, inID)
	require.Error(t, gerr)
}

// TestDisk_SEC_D_RegisterAndUnregisterIntent — Disk Create/Delete write intents
// in the writer-tx (parity with Instance; covers compute_disk FGA type).
func TestDisk_SEC_D_RegisterAndUnregisterIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	diskRepo := repo.NewDiskRepo(pool)
	dID := ids.NewID(ids.PrefixDisk)
	projectID := "proj-ccccccccccccccccc"
	_, err = diskRepo.Insert(ctx, &domain.Disk{ID: dID, ProjectID: projectID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), Name: "disk-sec-d", TypeID: "network-ssd", ZoneID: "ru-central1-a", Size: 4194304, BlockSize: 4096, Status: domain.DiskStatusReady})
	require.NoError(t, err)

	regRows := queryFGARegisterRows(ctx, t, pool, dID)
	require.Len(t, regRows, 1)
	assert.Equal(t, fgaintent.EventRegister, regRows[0].eventType)
	assert.Equal(t, "compute_disk:"+dID, payloadTuples(t, regRows[0].payload)[0].Object)

	require.NoError(t, diskRepo.Delete(ctx, dID))
	allRows := queryFGARegisterRows(ctx, t, pool, dID)
	var sawUnreg bool
	for _, r := range allRows {
		if r.eventType == fgaintent.EventUnregister {
			sawUnreg = true
			assert.Equal(t, "compute_disk:"+dID, payloadTuples(t, r.payload)[0].Object)
		}
	}
	assert.True(t, sawUnreg, "Disk.Delete writes fga.unregister intent")
}
