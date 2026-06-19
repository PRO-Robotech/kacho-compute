package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
	"github.com/PRO-Robotech/kacho-corelib/outbox"
)

// computeOutboxTable — имя таблицы outbox в kacho_compute DB.
const computeOutboxTable = "compute_outbox"

// fgaRegisterOutboxTable — таблица FGA-register-intent (SEC-D, миграция 0010).
const fgaRegisterOutboxTable = "compute_fga_register_outbox"

// emitCompute — обёртка над outbox.Emit с фиксированной таблицей compute_outbox.
// Должна вызываться внутри той же tx, что и INSERT/UPDATE/DELETE на ресурсной
// таблице (атомарность). Trigger compute_outbox_notify_trg на каждый INSERT
// шлёт pg_notify('compute_outbox', sequence_no::text). kind ∈ {Instance, Disk, Image, Snapshot}.
func emitCompute(ctx context.Context, tx pgx.Tx, kind, id, eventType string, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	return outbox.Emit(ctx, tx, computeOutboxTable, kind, id, eventType, payload)
}

// domainToMap конвертирует произвольный domain-объект в map[string]any через
// JSON round-trip. При ошибке возвращает пустую map (lenient — outbox event
// важнее content-корректности).
func domainToMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// emitFGARegisterIntent writes one FGA-register/unregister intent row into
// compute_fga_register_outbox IN THE SAME tx as the resource Insert/Update/Delete
// (SEC-D transactional outbox — no dual-write). event ∈ {fga.register,
// fga.unregister}; kind ∈ {Instance, Disk, Image, Snapshot}. The payload carries
// the project-hierarchy owner-tuple set AND (epic RSAB β) the owner's labels +
// parent-scope (project) so the register-drainer can feed IAM resource_mirror.
// labels may be nil/empty (graceful — empty mirror labels). parent_account_id is
// left empty: compute does not resolve project→account on the resource hot-path
// (IAM handles an empty parent gracefully). Unknown kind / empty id / empty
// projectID → no row is written (caller's resource still commits; an unmappable
// kind simply has no FGA hierarchy to register — fail-safe, never an orphan
// intent). An INSERT here fires the NOTIFY trigger waking the register-drainer;
// if the surrounding tx aborts, the intent rolls back atomically.
func emitFGARegisterIntent(ctx context.Context, tx pgx.Tx, event, kind, resourceID, projectID string, labels map[string]string) error {
	tuple, ok := fgaintent.ProjectHierarchyTuple(kind, resourceID, projectID)
	if !ok {
		return nil
	}
	b, err := fgaintent.Encode(fgaintent.Payload{
		Tuples:          []fgaintent.Tuple{tuple},
		Labels:          labels,
		ParentProjectID: projectID,
	})
	if err != nil {
		return fmt.Errorf("encode fga intent: %w", err)
	}
	// Stamp the β-hardening monotonic source_version into the payload from the DB
	// clock (now()) AT INSERT TIME, inside this writer-tx — the exact instant the
	// source-state is recorded. jsonb_set merges it into the encoded payload so the
	// register-drainer forwards it to IAM.RegisterResource.source_version
	// (last-source-state-wins). Compute has no per-row updated_at; the intent-emit
	// now() is the correct, least-invasive per-object marker and matches the row's
	// own created_at default (same transaction_timestamp()).
	_, err = tx.Exec(ctx,
		fmt.Sprintf(`INSERT INTO %s (event_type, resource_kind, resource_id, payload)
		             VALUES ($1, $2, $3, jsonb_set($4::jsonb, '{source_version}', to_jsonb(now())))`, fgaRegisterOutboxTable),
		event, kind, resourceID, b)
	if err != nil {
		return fmt.Errorf("emit fga register intent: %w", err)
	}
	return nil
}

func diskPayload(d *domain.Disk) map[string]any          { return domainToMap(d) }
func imagePayload(i *domain.Image) map[string]any        { return domainToMap(i) }
func snapshotPayload(s *domain.Snapshot) map[string]any  { return domainToMap(s) }
func instancePayload(in *domain.Instance) map[string]any { return domainToMap(in) }
