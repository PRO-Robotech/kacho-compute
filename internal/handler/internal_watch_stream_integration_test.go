// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/migrations"
)

// fakeWatchStream — минимальный computev1.InternalWatchService_WatchServer
// (= grpc.ServerStreamingServer[Event]) для unit/integration-теста streamSince:
// собирает отправленные Event'ы, отдаёт управляемый Context.
type fakeWatchStream struct {
	ctx  context.Context
	sent []*computev1.Event
}

func (s *fakeWatchStream) Send(ev *computev1.Event) error { s.sent = append(s.sent, ev); return nil }
func (s *fakeWatchStream) Context() context.Context       { return s.ctx }
func (s *fakeWatchStream) SetHeader(metadata.MD) error    { return nil }
func (s *fakeWatchStream) SendHeader(metadata.MD) error   { return nil }
func (s *fakeWatchStream) SetTrailer(metadata.MD)         {}
func (s *fakeWatchStream) SendMsg(any) error              { return nil }
func (s *fakeWatchStream) RecvMsg(any) error              { return io.EOF }

// setupWatchDB — testcontainers Postgres 16 + goose-миграции (compute_outbox
// живёт в 0001_initial). Возвращает dsn.
func setupWatchDB(t *testing.T) string {
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

// TestIntegration_WatchStreamSince_BatchBoundary — cursor корректно продвигается
// через границу catchupBatchSize (250 > 100): все события доставлены строго по
// возрастанию sequence_no, ни одно не потеряно/не задублировано на стыке батчей.
func TestIntegration_WatchStreamSince_BatchBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupWatchDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const total = 250 // > 2× catchupBatchSize (100) → пересекает 2 границы батча
	for i := 0; i < total; i++ {
		_, err := pool.Exec(ctx,
			`INSERT INTO compute_outbox (resource_kind, resource_id, event_type, payload) VALUES ($1,$2,$3,$4::jsonb)`,
			"Instance", "epi-"+padID(i), "CREATED", `{"i":1}`)
		require.NoError(t, err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	h := NewInternalWatchHandler(pool, dsn, slog.Default(), 0)
	fs := &fakeWatchStream{ctx: ctx}
	newCursor, err := h.streamSince(ctx, conn, 0, nil, fs)
	require.NoError(t, err)

	require.Len(t, fs.sent, total, "all outbox rows must be delivered across batch boundaries")
	// strictly ascending, contiguous cursor advancement.
	var prev int64
	for i, ev := range fs.sent {
		require.Greater(t, ev.GetSequenceNo(), prev, "sequence_no must be strictly ascending (idx %d)", i)
		prev = ev.GetSequenceNo()
	}
	assert.Equal(t, prev, newCursor, "returned cursor must equal last delivered sequence_no")
}

// TestIntegration_WatchStreamSince_KindsFilter — resource_kind = ANY($2) фильтр
// доставляет только запрошенные kind'ы.
func TestIntegration_WatchStreamSince_KindsFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupWatchDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	kinds := []string{"Instance", "Disk", "Image", "Instance", "Disk"}
	for i, k := range kinds {
		_, err := pool.Exec(ctx,
			`INSERT INTO compute_outbox (resource_kind, resource_id, event_type, payload) VALUES ($1,$2,$3,'{}'::jsonb)`,
			k, "r-"+padID(i), "CREATED")
		require.NoError(t, err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	h := NewInternalWatchHandler(pool, dsn, slog.Default(), 0)
	fs := &fakeWatchStream{ctx: ctx}
	_, err = h.streamSince(ctx, conn, 0, []string{"Disk"}, fs)
	require.NoError(t, err)

	require.Len(t, fs.sent, 2, "only Disk events must pass the kinds filter")
	for _, ev := range fs.sent {
		assert.Equal(t, "Disk", ev.GetResourceKind())
	}
}

// TestIntegration_WatchStreamSince_ResumeFromCursor — from_sequence_no resume:
// события с sequence_no <= cursor пропускаются.
func TestIntegration_WatchStreamSince_ResumeFromCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupWatchDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	var seqs []int64
	for i := 0; i < 5; i++ {
		var seq int64
		err := pool.QueryRow(ctx,
			`INSERT INTO compute_outbox (resource_kind, resource_id, event_type, payload) VALUES ('Instance',$1,'CREATED','{}'::jsonb) RETURNING sequence_no`,
			"epi-"+padID(i)).Scan(&seq)
		require.NoError(t, err)
		seqs = append(seqs, seq)
	}

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	h := NewInternalWatchHandler(pool, dsn, slog.Default(), 0)
	fs := &fakeWatchStream{ctx: ctx}
	// resume from the 3rd row → expect exactly the last 2 rows.
	_, err = h.streamSince(ctx, conn, seqs[2], nil, fs)
	require.NoError(t, err)

	require.Len(t, fs.sent, 2, "resume must skip rows with sequence_no <= cursor")
	assert.Equal(t, seqs[3], fs.sent[0].GetSequenceNo())
	assert.Equal(t, seqs[4], fs.sent[1].GetSequenceNo())
}

// TestIntegration_WatchStreamSince_BadPayloadFallback — не-object JSONB payload
// (json.Unmarshal→map падает) не роняет stream: событие доставляется с пустым
// Struct-payload (graceful degradation, не drop).
func TestIntegration_WatchStreamSince_BadPayloadFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupWatchDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// '[]'::jsonb — валидный JSONB, но НЕ object → structpb decode fails → fallback.
	_, err = pool.Exec(ctx,
		`INSERT INTO compute_outbox (resource_kind, resource_id, event_type, payload) VALUES ('Instance','epi-bad','CREATED','[]'::jsonb)`)
	require.NoError(t, err)

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()

	h := NewInternalWatchHandler(pool, dsn, slog.Default(), 0)
	fs := &fakeWatchStream{ctx: ctx}
	_, err = h.streamSince(ctx, conn, 0, nil, fs)
	require.NoError(t, err)

	require.Len(t, fs.sent, 1, "bad-payload row must still be delivered (fallback), not dropped")
	assert.Empty(t, fs.sent[0].GetPayload().GetFields(), "fallback payload must be an empty Struct")
}

// TestWatch_ResourceExhausted — cap на одновременные stream'ы: когда все слоты
// заняты, Watch немедленно возвращает ResourceExhausted (до любого DB-контакта).
func TestWatch_ResourceExhausted(t *testing.T) {
	h := NewInternalWatchHandler(nil, "", slog.Default(), 1)
	// Занимаем единственный слот — параллельный Watch должен отбиться.
	h.streamSlot <- struct{}{}

	err := h.Watch(&computev1.WatchRequest{}, &fakeWatchStream{ctx: context.Background()})
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}

// padID — детерминированный zero-padded суффикс id (для стабильного порядка вставки).
func padID(i int) string {
	const digits = "0123456789"
	b := []byte{digits[(i/100)%10], digits[(i/10)%10], digits[i%10]}
	return string(b)
}
