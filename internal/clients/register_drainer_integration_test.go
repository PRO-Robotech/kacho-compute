package clients_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/clients"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
)

// ---- fake InternalIAMService client (recorder) ----------------------------

type fakeIAMRegister struct {
	mu           sync.Mutex
	registered   []*iamv1.RegisterResourceRequest
	unregistered []*iamv1.UnregisterResourceRequest
	// failN: return errCode on the first failN RegisterResource calls, then OK.
	failN   int32
	errCode codes.Code
	// perm: always return errCode (poison path).
	perm      bool
	callCount int32
}

func (f *fakeIAMRegister) RegisterResource(_ context.Context, in *iamv1.RegisterResourceRequest, _ ...grpc.CallOption) (*iamv1.RegisterResourceResponse, error) {
	atomic.AddInt32(&f.callCount, 1)
	if f.perm {
		return nil, status.Error(f.errCode, "permanent")
	}
	if atomic.AddInt32(&f.failN, -1) >= 0 {
		return nil, status.Error(f.errCode, "transient down")
	}
	f.mu.Lock()
	f.registered = append(f.registered, in)
	f.mu.Unlock()
	return &iamv1.RegisterResourceResponse{}, nil
}

func (f *fakeIAMRegister) UnregisterResource(_ context.Context, in *iamv1.UnregisterResourceRequest, _ ...grpc.CallOption) (*iamv1.UnregisterResourceResponse, error) {
	atomic.AddInt32(&f.callCount, 1)
	f.mu.Lock()
	f.unregistered = append(f.unregistered, in)
	f.mu.Unlock()
	return &iamv1.UnregisterResourceResponse{}, nil
}

func (f *fakeIAMRegister) registeredCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registered)
}

// ---- test harness ---------------------------------------------------------

func setupDrainerDB(t *testing.T) *pgxpool.Pool {
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
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(db, "."))
	_ = db.Close()

	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

const fgaRegisterTable = "compute_fga_register_outbox"
const fgaRegisterChannel = "compute_fga_register_outbox"

func insertIntent(ctx context.Context, t *testing.T, pool *pgxpool.Pool, eventType, kind, resourceID string, tuples ...fgaintent.Tuple) {
	t.Helper()
	b, err := fgaintent.Encode(fgaintent.Payload{Tuples: tuples})
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO compute_fga_register_outbox (event_type, resource_kind, resource_id, payload) VALUES ($1,$2,$3,$4)`,
		eventType, kind, resourceID, b)
	require.NoError(t, err)
}

func insertIntentPayload(ctx context.Context, t *testing.T, pool *pgxpool.Pool, eventType, kind, resourceID string, p fgaintent.Payload) {
	t.Helper()
	b, err := fgaintent.Encode(p)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO compute_fga_register_outbox (event_type, resource_kind, resource_id, payload) VALUES ($1,$2,$3,$4)`,
		eventType, kind, resourceID, b)
	require.NoError(t, err)
}

func newDrainer(t *testing.T, pool *pgxpool.Pool, applier *clients.IAMRegisterApplier) *drainer.Drainer[fgaintent.Payload] {
	t.Helper()
	d, err := drainer.New[fgaintent.Payload](
		pool,
		drainer.Config{
			Table:       "public." + fgaRegisterTable,
			Channel:     fgaRegisterChannel,
			BatchSize:   16,
			MaxAttempts: 5,
			BackoffMin:  100 * time.Millisecond,
			BackoffMax:  500 * time.Millisecond,
		},
		func(b []byte) (fgaintent.Payload, error) {
			p, err := fgaintent.Decode(b)
			if err != nil {
				return fgaintent.Payload{}, errors.Join(drainer.ErrPermanent, err)
			}
			return p, nil
		},
		applier.Apply,
		nil,
	)
	require.NoError(t, err)
	return d
}

func countSent(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (sent, pending int) {
	t.Helper()
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM compute_fga_register_outbox WHERE sent_at IS NOT NULL`).Scan(&sent))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM compute_fga_register_outbox WHERE sent_at IS NULL`).Scan(&pending))
	return
}

// TestRegisterDrainer_SEC_D_09_HappyApply — SEC-D-09: drainer applies a register
// intent via IAM.RegisterResource, intent marked sent.
func TestRegisterDrainer_SEC_D_09_HappyApply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)
	fake := &fakeIAMRegister{}
	applier := clients.NewIAMRegisterApplierWithClient(fake)

	insertIntent(ctx, t, pool, fgaintent.EventRegister, "Instance", "epd-1",
		fgaintent.Tuple{SubjectID: "project:proj-x", Relation: "project", Object: "compute_instance:epd-1"})

	d := newDrainer(t, pool, applier)
	go func() { _ = d.Run(ctx) }()

	require.Eventually(t, func() bool {
		sent, _ := countSent(ctx, t, pool)
		return sent == 1 && fake.registeredCount() == 1
	}, 3*time.Second, 50*time.Millisecond)

	require.Len(t, fake.registered, 1)
	assert.Equal(t, "compute_instance:epd-1", fake.registered[0].Object)
}

// TestRegisterDrainer_SEC_D_10_UnregisterApply — SEC-D-10: unregister intent →
// IAM.UnregisterResource.
func TestRegisterDrainer_SEC_D_10_UnregisterApply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)
	fake := &fakeIAMRegister{}
	applier := clients.NewIAMRegisterApplierWithClient(fake)

	insertIntent(ctx, t, pool, fgaintent.EventUnregister, "Instance", "epd-2",
		fgaintent.Tuple{SubjectID: "project:proj-y", Relation: "project", Object: "compute_instance:epd-2"})

	d := newDrainer(t, pool, applier)
	go func() { _ = d.Run(ctx) }()

	require.Eventually(t, func() bool {
		sent, _ := countSent(ctx, t, pool)
		f := func() int { fake.mu.Lock(); defer fake.mu.Unlock(); return len(fake.unregistered) }
		return sent == 1 && f() == 1
	}, 3*time.Second, 50*time.Millisecond)
	assert.Equal(t, "compute_instance:epd-2", fake.unregistered[0].Object)
}

// TestRegisterDrainer_SEC_D_11_IAMDownThenRecover (КРИТИЧНО) — SEC-D-11: IAM
// Unavailable on first N calls → intent stays durable (sent_at NULL, last_error
// LIKE %Unavailable%), then recovers and is applied within the window. Tuple is
// never lost.
func TestRegisterDrainer_SEC_D_11_IAMDownThenRecover(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)
	fake := &fakeIAMRegister{failN: 2, errCode: codes.Unavailable}
	applier := clients.NewIAMRegisterApplierWithClient(fake)

	insertIntent(ctx, t, pool, fgaintent.EventRegister, "Instance", "epd-down",
		fgaintent.Tuple{SubjectID: "project:proj-z", Relation: "project", Object: "compute_instance:epd-down"})

	d := newDrainer(t, pool, applier)
	go func() { _ = d.Run(ctx) }()

	// During the down-window: intent durable, sent_at NULL, last_error set.
	require.Eventually(t, func() bool {
		var lastErr *string
		require.NoError(t, pool.QueryRow(ctx, `SELECT last_error FROM compute_fga_register_outbox WHERE resource_id = 'epd-down'`).Scan(&lastErr))
		return lastErr != nil && containsCI(*lastErr, "Unavailable")
	}, 3*time.Second, 50*time.Millisecond)

	// After recovery: applied, sent_at set, exactly one successful register.
	require.Eventually(t, func() bool {
		sent, _ := countSent(ctx, t, pool)
		return sent == 1 && fake.registeredCount() == 1
	}, 5*time.Second, 50*time.Millisecond)
}

// TestRegisterDrainer_SEC_D_13_ConcurrentTwoReplicas — SEC-D-13: two drainer
// replicas on the same DB apply each of 20 intents exactly once (CAS-claim /
// SKIP-LOCKED). no double-apply, no miss.
func TestRegisterDrainer_SEC_D_13_ConcurrentTwoReplicas(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)
	fake := &fakeIAMRegister{}
	applier := clients.NewIAMRegisterApplierWithClient(fake)

	const n = 20
	for i := 0; i < n; i++ {
		id := "epd-conc-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		insertIntent(ctx, t, pool, fgaintent.EventRegister, "Instance", id,
			fgaintent.Tuple{SubjectID: "project:proj-c", Relation: "project", Object: "compute_instance:" + id})
	}

	d1 := newDrainer(t, pool, applier)
	d2 := newDrainer(t, pool, applier)
	go func() { _ = d1.Run(ctx) }()
	go func() { _ = d2.Run(ctx) }()

	require.Eventually(t, func() bool {
		sent, pending := countSent(ctx, t, pool)
		return sent == n && pending == 0
	}, 5*time.Second, 50*time.Millisecond)

	assert.Equal(t, n, fake.registeredCount(), "exactly-once across replicas (no double-apply, no miss)")
}

// TestRegisterDrainer_SEC_D_14_PermanentPoison — SEC-D-14: IAM InvalidArgument →
// poison (attempt_count >= MaxAttempts, sent_at NULL), drainer keeps processing
// other rows.
func TestRegisterDrainer_SEC_D_14_PermanentPoison(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)
	fake := &fakeIAMRegister{perm: true, errCode: codes.InvalidArgument}
	applier := clients.NewIAMRegisterApplierWithClient(fake)

	insertIntent(ctx, t, pool, fgaintent.EventRegister, "Instance", "epd-poison",
		fgaintent.Tuple{SubjectID: "bad", Relation: "bad", Object: "bad"})

	d := newDrainer(t, pool, applier)
	go func() { _ = d.Run(ctx) }()

	require.Eventually(t, func() bool {
		var attempts int
		var sentAt *time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT attempt_count, sent_at FROM compute_fga_register_outbox WHERE resource_id = 'epd-poison'`).Scan(&attempts, &sentAt))
		return sentAt == nil && attempts >= 5
	}, 3*time.Second, 50*time.Millisecond)
}

// TestRegisterDrainer_Beta01_ForwardsLabelsAndParent — β-01: the applier maps the
// intent payload's mirror fields (labels + parent-scope) onto the forwarded
// IAM.RegisterResourceRequest, so kacho-iam can populate resource_mirror.
func TestRegisterDrainer_Beta01_ForwardsLabelsAndParent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := setupDrainerDB(t)
	fake := &fakeIAMRegister{}
	applier := clients.NewIAMRegisterApplierWithClient(fake)

	insertIntentPayload(ctx, t, pool, fgaintent.EventRegister, "Instance", "epd-mirror", fgaintent.Payload{
		Tuples:          []fgaintent.Tuple{{SubjectID: "project:prj-P", Relation: "project", Object: "compute_instance:epd-mirror"}},
		Labels:          map[string]string{"env": "dev", "team": "core"},
		ParentProjectID: "prj-P",
		ParentAccountID: "acc-A",
	})

	d := newDrainer(t, pool, applier)
	go func() { _ = d.Run(ctx) }()

	require.Eventually(t, func() bool {
		return fake.registeredCount() == 1
	}, 3*time.Second, 50*time.Millisecond)

	req := fake.registered[0]
	assert.Equal(t, "compute_instance:epd-mirror", req.GetObject())
	assert.Equal(t, map[string]string{"env": "dev", "team": "core"}, req.GetLabels())
	assert.Equal(t, "prj-P", req.GetParentProjectId())
	assert.Equal(t, "acc-A", req.GetParentAccountId())
}

func containsCI(s, sub string) bool {
	return len(s) >= len(sub) && (indexCI(s, sub) >= 0)
}

func indexCI(s, sub string) int {
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + ('a' - 'A')
		}
		return b
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
