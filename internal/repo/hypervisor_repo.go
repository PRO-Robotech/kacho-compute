package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-corelib/validate"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// HypervisorRepo — реализация service.HypervisorRepo поверх pgxpool.
type HypervisorRepo struct {
	pool *pgxpool.Pool
}

// NewHypervisorRepo создаёт HypervisorRepo.
func NewHypervisorRepo(pool *pgxpool.Pool) *HypervisorRepo { return &HypervisorRepo{pool: pool} }

const hvCols = `id, zone_id, node_index, fqdn, state, capacity_vcpus, capacity_memory_bytes, capacity_instances, created_at, updated_at`

func scanHypervisor(row interface{ Scan(...any) error }) (*domain.Hypervisor, error) {
	var h domain.Hypervisor
	var nodeIdx int32
	var stateName string
	if err := row.Scan(&h.ID, &h.ZoneID, &nodeIdx, &h.FQDN, &stateName,
		&h.Capacity.VCPUs, &h.Capacity.MemoryBytes, &h.Capacity.Instances, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return nil, err
	}
	h.NodeIndex = uint32(nodeIdx)
	h.State = hvStateFromName(stateName)
	return &h, nil
}

// Get возвращает гипервизор по id.
func (r *HypervisorRepo) Get(ctx context.Context, id string) (*domain.Hypervisor, error) {
	h, err := scanHypervisor(r.pool.QueryRow(ctx, `SELECT `+hvCols+` FROM hypervisors WHERE id = $1`, id))
	if err != nil {
		return nil, wrapPgErr(err, "Hypervisor", id)
	}
	return h, nil
}

// List возвращает гипервизоры с опц. фильтром по zone/state и cursor-пагинацией по id.
func (r *HypervisorRepo) List(ctx context.Context, zoneID string, state domain.HypervisorState, p service.Pagination) ([]*domain.Hypervisor, string, error) {
	pageSize, err := validate.PageSize("page_size", p.PageSize)
	if err != nil {
		return nil, "", err
	}
	args := []any{}
	conds := []string{}
	if zoneID != "" {
		args = append(args, zoneID)
		conds = append(conds, fmt.Sprintf("zone_id = $%d", len(args)))
	}
	if state != domain.HypervisorStateUnspecified {
		args = append(args, hvStateName(state))
		conds = append(conds, fmt.Sprintf("state = $%d", len(args)))
	}
	if p.PageToken != "" {
		_, cursorID, derr := decodePageToken(p.PageToken)
		if derr != nil {
			return nil, "", invalidPageTokenErr(derr)
		}
		args = append(args, cursorID)
		conds = append(conds, fmt.Sprintf("id > $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + joinAnd(conds)
	}
	args = append(args, pageSize+1)
	q := fmt.Sprintf(`SELECT %s FROM hypervisors %s ORDER BY id ASC LIMIT $%d`, hvCols, where, len(args))
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", wrapPgErr(err, "Hypervisor", "")
	}
	defer rows.Close()
	var out []*domain.Hypervisor
	for rows.Next() {
		h, err := scanHypervisor(rows)
		if err != nil {
			return nil, "", wrapPgErr(err, "Hypervisor", "")
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrapPgErr(err, "Hypervisor", "")
	}
	var next string
	if int64(len(out)) > pageSize {
		last := out[pageSize-1]
		next = encodePageToken(last.CreatedAt, last.ID)
		out = out[:pageSize]
	}
	return out, next, nil
}

// Insert вставляет гипервизор, атомарно аллоцируя node_index (popped из free-list,
// иначе nextval — COALESCE в Postgres коротко замыкается; конкурентно безопасно
// через row-lock на DELETE).
func (r *HypervisorRepo) Insert(ctx context.Context, h *domain.Hypervisor) (*domain.Hypervisor, error) {
	const q = `
		WITH popped AS (
			DELETE FROM hypervisor_node_index_free WHERE id = (SELECT id FROM hypervisor_node_index_free ORDER BY id LIMIT 1) RETURNING id
		)
		INSERT INTO hypervisors (id, zone_id, node_index, fqdn, state, capacity_vcpus, capacity_memory_bytes, capacity_instances, created_at, updated_at)
		VALUES ($1, $2, COALESCE((SELECT id FROM popped), nextval('hypervisor_node_index_seq')::int), $3, $4, $5, $6, $7, now(), now())
		RETURNING ` + hvCols
	created, err := scanHypervisor(r.pool.QueryRow(ctx, q,
		h.ID, h.ZoneID, h.FQDN, hvStateName(h.State), h.Capacity.VCPUs, h.Capacity.MemoryBytes, h.Capacity.Instances))
	if err != nil {
		return nil, wrapPgErr(err, "Hypervisor", h.ID)
	}
	return created, nil
}

// Update обновляет state/capacity/updated_at гипервизора.
func (r *HypervisorRepo) Update(ctx context.Context, h *domain.Hypervisor) (*domain.Hypervisor, error) {
	const q = `
		UPDATE hypervisors SET state=$2, capacity_vcpus=$3, capacity_memory_bytes=$4, capacity_instances=$5, updated_at=now()
		WHERE id=$1
		RETURNING ` + hvCols
	updated, err := scanHypervisor(r.pool.QueryRow(ctx, q,
		h.ID, hvStateName(h.State), h.Capacity.VCPUs, h.Capacity.MemoryBytes, h.Capacity.Instances))
	if err != nil {
		return nil, wrapPgErr(err, "Hypervisor", h.ID)
	}
	return updated, nil
}

// Delete снимает хост с регистрации и возвращает его node_index во free-list.
func (r *HypervisorRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `
		WITH d AS (DELETE FROM hypervisors WHERE id = $1 RETURNING node_index)
		INSERT INTO hypervisor_node_index_free (id) SELECT node_index FROM d ON CONFLICT DO NOTHING`, id)
	if err != nil {
		return wrapPgErr(err, "Hypervisor", id)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrNotFound
	}
	return nil
}

func joinAnd(conds []string) string {
	out := conds[0]
	for _, c := range conds[1:] {
		out += " AND " + c
	}
	return out
}

func hvStateName(s domain.HypervisorState) string {
	switch s {
	case domain.HypervisorStateReady:
		return "READY"
	case domain.HypervisorStateCordoned:
		return "CORDONED"
	case domain.HypervisorStateDraining:
		return "DRAINING"
	case domain.HypervisorStateDown:
		return "DOWN"
	default:
		return "STATE_UNSPECIFIED"
	}
}

func hvStateFromName(s string) domain.HypervisorState {
	switch s {
	case "READY":
		return domain.HypervisorStateReady
	case "CORDONED":
		return domain.HypervisorStateCordoned
	case "DRAINING":
		return domain.HypervisorStateDraining
	case "DOWN":
		return domain.HypervisorStateDown
	default:
		return domain.HypervisorStateUnspecified
	}
}
