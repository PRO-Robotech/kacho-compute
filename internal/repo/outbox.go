package repo

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-corelib/outbox"
)

// computeOutboxTable — имя таблицы outbox в kacho_compute DB.
const computeOutboxTable = "compute_outbox"

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

func diskPayload(d *domain.Disk) map[string]any          { return domainToMap(d) }
func imagePayload(i *domain.Image) map[string]any        { return domainToMap(i) }
func snapshotPayload(s *domain.Snapshot) map[string]any  { return domainToMap(s) }
func instancePayload(in *domain.Instance) map[string]any { return domainToMap(in) }
