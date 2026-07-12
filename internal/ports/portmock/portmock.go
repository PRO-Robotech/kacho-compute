// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package portmock содержит in-memory fake-реализации port-интерфейсов из
// `internal/ports` плюс helper'ы для ожидания async-Operation'ов. Используется
// unit-тестами `internal/service` и `internal/handler`.
//
// Зависит только от `internal/ports`, `internal/domain` и `kacho-corelib/operations`
// — НЕ от `internal/service`, поэтому white-box service-тесты (`package service`)
// могут его импортировать без import-cycle. Зеркалит kacho-vpc/internal/ports/portmock.
package portmock

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// ---- DiskRepo ----

// DiskRepo — in-memory DiskRepo.
type DiskRepo struct {
	mu   sync.Mutex
	data map[string]*domain.Disk
	// LastUpdateEmitLabels — последнее значение emitLabelsRegister, переданное в
	// Update, для проверки labels-gated mirror-эмита use-case-тестом.
	LastUpdateEmitLabels *bool
}

// NewDiskRepo создаёт пустой DiskRepo.
func NewDiskRepo() *DiskRepo {
	return &DiskRepo{data: make(map[string]*domain.Disk)}
}

// Seed добавляет диск напрямую (для fixture'ов).
func (r *DiskRepo) Seed(d *domain.Disk) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[d.ID] = d
}

// Get возвращает диск по id.
func (r *DiskRepo) Get(_ context.Context, id string) (*domain.Disk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return d, nil
}

// List возвращает диски по folder.
//
// Honors AllowedIDs — if non-nil, only return ids contained in the allow-list
// (empty allow-list → empty result, NO repo scan).
func (r *DiskRepo) List(_ context.Context, f ports.DiskFilter, _ ports.Pagination) ([]*domain.Disk, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.AllowedIDs != nil && len(f.AllowedIDs) == 0 {
		return nil, "", nil
	}
	allow := allowSet(f.AllowedIDs)
	var out []*domain.Disk
	for _, d := range r.data {
		if f.ProjectID != "" && d.ProjectID != f.ProjectID {
			continue
		}
		if allow != nil {
			if _, ok := allow[d.ID]; !ok {
				continue
			}
		}
		out = append(out, d)
	}
	return out, "", nil
}

// allowSet — convert []string to set; nil means "no filter".
func allowSet(ids []string) map[string]struct{} {
	if ids == nil {
		return nil
	}
	s := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

// Insert вставляет диск.
func (r *DiskRepo) Insert(_ context.Context, d *domain.Disk) (*domain.Disk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.Name != "" {
		for _, x := range r.data {
			if x.ProjectID == d.ProjectID && x.Name == d.Name {
				return nil, ports.ErrAlreadyExists
			}
		}
	}
	r.data[d.ID] = d
	return d, nil
}

// Update обновляет диск. Записывает emitLabelsRegister в LastUpdateEmitLabels
// для проверки use-case-тестом.
func (r *DiskRepo) Update(_ context.Context, d *domain.Disk, emitLabelsRegister bool, _ []string) (*domain.Disk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	flag := emitLabelsRegister
	r.LastUpdateEmitLabels = &flag
	if _, ok := r.data[d.ID]; !ok {
		return nil, ports.ErrNotFound
	}
	r.data[d.ID] = d
	return d, nil
}

// Delete удаляет диск. Storage-split: compute Disk больше не «attached» локально —
// in-use гейта нет.
func (r *DiskRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[id]; !ok {
		return ports.ErrNotFound
	}
	delete(r.data, id)
	return nil
}

// SetZoneIfDetached меняет zone_id (Relocate). Storage-split: compute Disk всегда
// detached локально — attach-гейта нет.
func (r *DiskRepo) SetZoneIfDetached(_ context.Context, id, zoneID string) (*domain.Disk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	d.ZoneID = zoneID
	return d, nil
}

// ---- ImageRepo ----

// ImageRepo — in-memory ImageRepo.
type ImageRepo struct {
	mu   sync.Mutex
	data map[string]*domain.Image
	// LastUpdateEmitLabels — последнее emitLabelsRegister из Update.
	LastUpdateEmitLabels *bool
}

// NewImageRepo создаёт пустой ImageRepo.
func NewImageRepo() *ImageRepo { return &ImageRepo{data: make(map[string]*domain.Image)} }

// Seed добавляет образ напрямую.
func (r *ImageRepo) Seed(i *domain.Image) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[i.ID] = i
}

// Get возвращает образ по id.
func (r *ImageRepo) Get(_ context.Context, id string) (*domain.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return i, nil
}

// GetLatestByFamily возвращает образ с max created_at в family.
func (r *ImageRepo) GetLatestByFamily(_ context.Context, folderID, family string) (*domain.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var best *domain.Image
	for _, i := range r.data {
		if i.ProjectID != folderID || i.Family != family {
			continue
		}
		if best == nil || i.CreatedAt.After(best.CreatedAt) {
			best = i
		}
	}
	if best == nil {
		return nil, ports.ErrNotFound
	}
	return best, nil
}

// List возвращает образы по folder. Honors AllowedIDs.
func (r *ImageRepo) List(_ context.Context, f ports.ImageFilter, _ ports.Pagination) ([]*domain.Image, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.AllowedIDs != nil && len(f.AllowedIDs) == 0 {
		return nil, "", nil
	}
	allow := allowSet(f.AllowedIDs)
	var out []*domain.Image
	for _, i := range r.data {
		if f.ProjectID != "" && i.ProjectID != f.ProjectID {
			continue
		}
		if allow != nil {
			if _, ok := allow[i.ID]; !ok {
				continue
			}
		}
		out = append(out, i)
	}
	return out, "", nil
}

// Insert вставляет образ.
func (r *ImageRepo) Insert(_ context.Context, i *domain.Image) (*domain.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i.Name != "" {
		for _, x := range r.data {
			if x.ProjectID == i.ProjectID && x.Name == i.Name {
				return nil, ports.ErrAlreadyExists
			}
		}
	}
	r.data[i.ID] = i
	return i, nil
}

// Update обновляет образ. Записывает emitLabelsRegister.
func (r *ImageRepo) Update(_ context.Context, i *domain.Image, emitLabelsRegister bool, _ []string) (*domain.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	flag := emitLabelsRegister
	r.LastUpdateEmitLabels = &flag
	if _, ok := r.data[i.ID]; !ok {
		return nil, ports.ErrNotFound
	}
	r.data[i.ID] = i
	return i, nil
}

// Delete удаляет образ.
func (r *ImageRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[id]; !ok {
		return ports.ErrNotFound
	}
	delete(r.data, id)
	return nil
}

// ---- SnapshotRepo ----

// SnapshotRepo — in-memory SnapshotRepo.
type SnapshotRepo struct {
	mu   sync.Mutex
	data map[string]*domain.Snapshot
	// LastUpdateEmitLabels — последнее emitLabelsRegister из Update.
	LastUpdateEmitLabels *bool
}

// NewSnapshotRepo создаёт пустой SnapshotRepo.
func NewSnapshotRepo() *SnapshotRepo { return &SnapshotRepo{data: make(map[string]*domain.Snapshot)} }

// Seed добавляет снапшот напрямую.
func (r *SnapshotRepo) Seed(s *domain.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[s.ID] = s
}

// Get возвращает снапшот по id.
func (r *SnapshotRepo) Get(_ context.Context, id string) (*domain.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return s, nil
}

// List возвращает снапшоты по folder. Honors AllowedIDs.
func (r *SnapshotRepo) List(_ context.Context, f ports.SnapshotFilter, _ ports.Pagination) ([]*domain.Snapshot, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.AllowedIDs != nil && len(f.AllowedIDs) == 0 {
		return nil, "", nil
	}
	allow := allowSet(f.AllowedIDs)
	var out []*domain.Snapshot
	for _, s := range r.data {
		if f.ProjectID != "" && s.ProjectID != f.ProjectID {
			continue
		}
		if allow != nil {
			if _, ok := allow[s.ID]; !ok {
				continue
			}
		}
		out = append(out, s)
	}
	return out, "", nil
}

// Insert вставляет снапшот.
func (r *SnapshotRepo) Insert(_ context.Context, s *domain.Snapshot) (*domain.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.Name != "" {
		for _, x := range r.data {
			if x.ProjectID == s.ProjectID && x.Name == s.Name {
				return nil, ports.ErrAlreadyExists
			}
		}
	}
	r.data[s.ID] = s
	return s, nil
}

// Update обновляет снапшот. Записывает emitLabelsRegister.
func (r *SnapshotRepo) Update(_ context.Context, s *domain.Snapshot, emitLabelsRegister bool, _ []string) (*domain.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	flag := emitLabelsRegister
	r.LastUpdateEmitLabels = &flag
	if _, ok := r.data[s.ID]; !ok {
		return nil, ports.ErrNotFound
	}
	r.data[s.ID] = s
	return s, nil
}

// Delete удаляет снапшот.
func (r *SnapshotRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[id]; !ok {
		return ports.ErrNotFound
	}
	delete(r.data, id)
	return nil
}

// ---- InstanceRepo ----

// InstanceRepo — in-memory InstanceRepo.
type InstanceRepo struct {
	mu   sync.Mutex
	data map[string]*domain.Instance
	// LastUpdateEmitLabels — последнее значение emitLabelsRegister, переданное в
	// Update (epic RSAB β, D-β6). nil — Update ещё не вызывался. Позволяет
	// use-case-тесту проверить решение «labels ∈ mask → эмитить register-intent».
	LastUpdateEmitLabels *bool
}

// NewInstanceRepo создаёт пустой InstanceRepo.
func NewInstanceRepo() *InstanceRepo { return &InstanceRepo{data: make(map[string]*domain.Instance)} }

// Seed добавляет ВМ напрямую.
func (r *InstanceRepo) Seed(in *domain.Instance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[in.ID] = in
}

// Get возвращает ВМ по id. Отдаёт shallow-КОПИЮ (не live-указатель) — зеркалит
// pg-адаптер, где каждый Get — свежий scan строки: конкурентные worker'ы,
// заполняющие read-only NIC-зеркало (applyNicMirror пишет in.NetworkInterfaces),
// не делят один *domain.Instance (иначе data-race на общем указателе, чего в
// проде нет).
func (r *InstanceRepo) Get(_ context.Context, id string) (*domain.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *in
	return &cp, nil
}

// List возвращает ВМ по folder.
func (r *InstanceRepo) List(_ context.Context, f ports.InstanceFilter, _ ports.Pagination) ([]*domain.Instance, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.AllowedIDs != nil && len(f.AllowedIDs) == 0 {
		return nil, "", nil
	}
	allow := allowSet(f.AllowedIDs)
	var out []*domain.Instance
	for _, in := range r.data {
		if f.ProjectID != "" && in.ProjectID != f.ProjectID {
			continue
		}
		if allow != nil {
			if _, ok := allow[in.ID]; !ok {
				continue
			}
		}
		out = append(out, in)
	}
	return out, "", nil
}

// Insert вставляет строку ВМ (без привязок — storage-split).
func (r *InstanceRepo) Insert(_ context.Context, in *domain.Instance) (*domain.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if in.Name != "" {
		for _, x := range r.data {
			if x.ProjectID == in.ProjectID && x.Name == in.Name {
				return nil, ports.ErrAlreadyExists
			}
		}
	}
	r.data[in.ID] = in
	return in, nil
}

// Update обновляет ВМ. Записывает emitLabelsRegister в LastUpdateEmitLabels
// (epic RSAB β, D-β6) для проверки use-case-тестом.
func (r *InstanceRepo) Update(_ context.Context, in *domain.Instance, emitLabelsRegister bool, _ []string) (*domain.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	flag := emitLabelsRegister
	r.LastUpdateEmitLabels = &flag
	if _, ok := r.data[in.ID]; !ok {
		return nil, ports.ErrNotFound
	}
	r.data[in.ID] = in
	return in, nil
}

// SetStatusCAS — in-memory CAS: атомарно переводит status из expected в next.
// Если row не существует → ErrNotFound; если текущий status != expected →
// ErrFailedPrecondition (mirrors DB-уровень в repo.InstanceRepo.SetStatusCAS).
func (r *InstanceRepo) SetStatusCAS(_ context.Context, id string, expected, next domain.InstanceStatus) (*domain.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	if in.Status != expected {
		return nil, fmt.Errorf("%w: state transition not allowed from current status", ports.ErrFailedPrecondition)
	}
	in.Status = next
	return in, nil
}

// GateForAttach — CAS-гейт attach-саги: инстанс ∈ {RUNNING, STOPPED} → возвращает
// zone/project/name; иначе FailedPrecondition; нет инстанса → NotFound.
func (r *InstanceRepo) GateForAttach(_ context.Context, id string) (string, string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.data[id]
	if !ok {
		return "", "", "", fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, id)
	}
	if in.Status != domain.InstanceStatusRunning && in.Status != domain.InstanceStatusStopped {
		return "", "", "", fmt.Errorf("%w: Instance must be RUNNING or STOPPED", ports.ErrFailedPrecondition)
	}
	return in.ZoneID, in.ProjectID, in.Name, nil
}

// MarkDeleting переводит инстанс в DELETING (идемпотентно). Нет инстанса → NotFound.
func (r *InstanceRepo) MarkDeleting(_ context.Context, id string) (*domain.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("%w: Instance %s not found", ports.ErrNotFound, id)
	}
	in.Status = domain.InstanceStatusDeleting
	cp := *in
	return &cp, nil
}

// MergeMetadata атомарно применяет delete+upsert дельту (под r.mu — зеркалит
// row-level-lock атомарность DB-адаптера: read+merge+write под одним локом).
func (r *InstanceRepo) MergeMetadata(_ context.Context, id string, del []string, upsert map[string]string) (*domain.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	md := map[string]string{}
	for k, v := range in.Metadata {
		md[k] = v
	}
	for _, k := range del {
		delete(md, k)
	}
	for k, v := range upsert {
		md[k] = v
	}
	in.Metadata = md
	return in, nil
}

// Delete удаляет строку ВМ (финальный шаг delete-саги; привязки уже сняты в
// use-case через storage/vpc Detach). Нет инстанса → NotFound.
func (r *InstanceRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[id]; !ok {
		return ports.ErrNotFound
	}
	delete(r.data, id)
	return nil
}

// ---- DiskTypeRepo ----

// DiskTypeRepo — in-memory DiskTypeRepo.
type DiskTypeRepo struct {
	mu   sync.Mutex
	data map[string]*domain.DiskType
}

// NewDiskTypeRepo создаёт DiskTypeRepo с seed-типами (network-ssd по умолчанию).
func NewDiskTypeRepo(ids ...string) *DiskTypeRepo {
	r := &DiskTypeRepo{data: make(map[string]*domain.DiskType)}
	if len(ids) == 0 {
		ids = []string{"network-ssd", "network-hdd"}
	}
	for _, id := range ids {
		r.data[id] = &domain.DiskType{ID: id}
	}
	return r
}

// Get возвращает тип диска по id.
func (r *DiskTypeRepo) Get(_ context.Context, id string) (*domain.DiskType, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.data[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return t, nil
}

// List возвращает все типы дисков.
func (r *DiskTypeRepo) List(_ context.Context, _ ports.Pagination) ([]*domain.DiskType, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*domain.DiskType
	for _, t := range r.data {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, "", nil
}

// Insert вставляет тип диска.
func (r *DiskTypeRepo) Insert(_ context.Context, t *domain.DiskType) (*domain.DiskType, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[t.ID]; ok {
		return nil, ports.ErrAlreadyExists
	}
	r.data[t.ID] = t
	return t, nil
}

// Update обновляет тип диска.
func (r *DiskTypeRepo) Update(_ context.Context, t *domain.DiskType) (*domain.DiskType, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[t.ID]; !ok {
		return nil, ports.ErrNotFound
	}
	r.data[t.ID] = t
	return t, nil
}

// Delete удаляет тип диска.
func (r *DiskTypeRepo) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[id]; !ok {
		return ports.ErrNotFound
	}
	delete(r.data, id)
	return nil
}

// ---- ZoneRegistry ----

// ZoneRegistry — in-memory ports.ZoneRegistry (zone_id existence-check для
// Disk/Instance Create + Disk Relocate). В проде реализуется clients.GeoClient
// (geo.v1.ZoneService.Get) — Geography принадлежит kacho-geo.
type ZoneRegistry struct {
	mu   sync.Mutex
	data map[string]struct{} // set of known zoneIDs (existence-check)
}

// NewZoneRegistry создаёт ZoneRegistry с seed-зонами (ru-central1-{a,b,d} по умолчанию).
func NewZoneRegistry(ids ...string) *ZoneRegistry {
	r := &ZoneRegistry{data: make(map[string]struct{})}
	if len(ids) == 0 {
		ids = []string{"ru-central1-a", "ru-central1-b", "ru-central1-d"}
	}
	for _, id := range ids {
		r.data[id] = struct{}{}
	}
	return r
}

// GetZone — реализация ports.ZoneRegistry: existence-check зоны по id
// (nil если зона засеяна, ErrNotFound при отсутствии).
func (r *ZoneRegistry) GetZone(_ context.Context, zoneID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.data[zoneID]; !ok {
		return ports.ErrNotFound
	}
	return nil
}

// ---- ProjectClient ----

// ProjectClient — fake ProjectClient. OK задаёт результат Exists().
type ProjectClient struct{ OK bool }

// Exists возвращает ProjectClient.OK.
func (c *ProjectClient) Exists(_ context.Context, _ string) (bool, error) { return c.OK, nil }

// ---- NicClient ----

// NicClient — in-memory fake ports.NicClient. Models the kacho-vpc side of the
// NIC↔Instance binding: a single-slot-per-NIC map with atomic (mutex-serialised)
// attach — enough to unit-test the compute saga-worker (auto-index, in-use CAS,
// idempotent replay, mirror-read) without a live kacho-vpc. AttachErrs / Err inject
// the peer error paths (zone-coherence FailedPrecondition, Unavailable fail-closed).
type NicClient struct {
	mu    sync.Mutex
	byNic map[string]ports.NicAttachment // nicID → current binding
	// AttachErrs — per-NIC injected Attach error (zone-coherence, in-use, …).
	AttachErrs map[string]error
	// Err — global injected error for Attach/Detach/ListByInstance (e.g. Unavailable).
	Err error
	// ListErr — injected error for ListByInstance only (mirror graceful-degrade test).
	ListErr error
}

// NewNicClient создаёт пустой fake NicClient.
func NewNicClient() *NicClient {
	return &NicClient{byNic: make(map[string]ports.NicAttachment), AttachErrs: make(map[string]error)}
}

// SeedZoneMismatch помечает NIC как zone-incoherent — Attach вернёт
// FailedPrecondition (S4-03), зеркалит kacho-vpc zone-coherence CAS-промах.
func (c *NicClient) SeedZoneMismatch(nicID, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AttachErrs[nicID] = grpcstatus.Error(grpccodes.FailedPrecondition, msg)
}

// Attach атомарно (под mutex) привязывает NIC к инстансу: auto-index (первый
// свободный слот при spec.Index==0), in-use-CAS (чужой инстанс → FailedPrecondition
// "NetworkInterface is in use"), идемпотентный replay (already-ours → OK).
func (c *NicClient) Attach(_ context.Context, spec ports.NicAttachSpec) (*ports.NicAttachment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return nil, c.Err
	}
	if e := c.AttachErrs[spec.NICID]; e != nil {
		return nil, e
	}
	if ex, ok := c.byNic[spec.NICID]; ok {
		if ex.InstanceID != spec.InstanceID {
			return nil, grpcstatus.Error(grpccodes.FailedPrecondition, "NetworkInterface is in use")
		}
		cp := ex
		return &cp, nil // idempotent replay: already ours
	}
	used := make(map[int32]bool)
	for _, a := range c.byNic {
		if a.InstanceID == spec.InstanceID {
			used[a.Index] = true
		}
	}
	idx := spec.Index
	for used[idx] {
		idx++
	}
	att := ports.NicAttachment{
		NICID: spec.NICID, InstanceID: spec.InstanceID, Index: idx,
		SubnetID: "sub-fake", PrimaryV4Address: fmt.Sprintf("10.0.0.%d", idx+2),
		MACAddress: fmt.Sprintf("00:11:22:33:44:%02d", idx),
	}
	c.byNic[spec.NICID] = att
	cp := att
	return &cp, nil
}

// Detach идемпотентно снимает привязку NIC↔instance (already-free → OK).
func (c *NicClient) Detach(_ context.Context, nicID, instanceID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	if ex, ok := c.byNic[nicID]; ok && ex.InstanceID == instanceID {
		delete(c.byNic, nicID)
	}
	return nil
}

// ListByInstance — batched read of NIC-привязок по instance-ids.
func (c *NicClient) ListByInstance(_ context.Context, instanceIDs []string) ([]ports.NicAttachment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ListErr != nil {
		return nil, c.ListErr
	}
	if c.Err != nil {
		return nil, c.Err
	}
	want := make(map[string]struct{}, len(instanceIDs))
	for _, id := range instanceIDs {
		want[id] = struct{}{}
	}
	var out []ports.NicAttachment
	for _, a := range c.byNic {
		if _, ok := want[a.InstanceID]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// ---- StorageClient ----

// StorageClient — in-memory fake ports.StorageClient. Models the kacho-storage side
// of the volume↔Instance attachment: a single-slot-per-volume map with atomic
// (mutex-serialised) attach — enough to unit-test the compute saga-worker (in-use CAS,
// idempotent replay, mirror-read) without a live kacho-storage. AttachErrs / Err inject
// the peer error paths (zone/project-coherence FailedPrecondition, Unavailable fail-closed).
type StorageClient struct {
	mu    sync.Mutex
	byVol map[string]ports.VolumeAttachmentInfo // volumeID → current attachment
	// AttachErrs — per-volume injected Attach error (zone/project-coherence, in-use, …).
	AttachErrs map[string]error
	// Err — global injected error for Attach/Detach/ListAttachments (e.g. Unavailable).
	Err error
	// ListErr — injected error for ListAttachments only (mirror graceful-degrade test).
	ListErr error
}

// NewStorageClient создаёт пустой fake StorageClient.
func NewStorageClient() *StorageClient {
	return &StorageClient{
		byVol:      make(map[string]ports.VolumeAttachmentInfo),
		AttachErrs: make(map[string]error),
	}
}

// SeedZoneMismatch помечает volume как zone-incoherent — Attach вернёт
// FailedPrecondition, зеркалит kacho-storage zone-coherence CAS-промах.
func (c *StorageClient) SeedZoneMismatch(volumeID, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AttachErrs[volumeID] = grpcstatus.Error(grpccodes.FailedPrecondition, msg)
}

// Attach атомарно (под mutex) привязывает volume к инстансу: in-use-CAS (чужой
// инстанс → FailedPrecondition "Volume is in use"), идемпотентный replay
// (already-ours → OK).
func (c *StorageClient) Attach(_ context.Context, spec ports.VolumeAttachSpec) (*ports.VolumeAttachmentInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return nil, c.Err
	}
	if e := c.AttachErrs[spec.VolumeID]; e != nil {
		return nil, e
	}
	if ex, ok := c.byVol[spec.VolumeID]; ok {
		if ex.InstanceID != spec.InstanceID {
			return nil, grpcstatus.Error(grpccodes.FailedPrecondition, "Volume is in use")
		}
		cp := ex
		return &cp, nil // idempotent replay: already ours
	}
	att := ports.VolumeAttachmentInfo{
		VolumeID:     spec.VolumeID,
		InstanceID:   spec.InstanceID,
		InstanceName: spec.InstanceName,
		DeviceName:   spec.DeviceName,
		IsBoot:       spec.IsBoot,
		Mode:         spec.Mode,
		AutoDelete:   spec.AutoDelete,
	}
	c.byVol[spec.VolumeID] = att
	cp := att
	return &cp, nil
}

// Detach идемпотентно снимает привязку volume↔instance (already-free → OK).
func (c *StorageClient) Detach(_ context.Context, volumeID, instanceID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return c.Err
	}
	if ex, ok := c.byVol[volumeID]; ok && ex.InstanceID == instanceID {
		delete(c.byVol, volumeID)
	}
	return nil
}

// ListAttachments — batched read volume-привязок по instance-ids.
func (c *StorageClient) ListAttachments(_ context.Context, instanceIDs []string) ([]ports.VolumeAttachmentInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ListErr != nil {
		return nil, c.ListErr
	}
	if c.Err != nil {
		return nil, c.Err
	}
	want := make(map[string]struct{}, len(instanceIDs))
	for _, id := range instanceIDs {
		want[id] = struct{}{}
	}
	var out []ports.VolumeAttachmentInfo
	for _, a := range c.byVol {
		if _, ok := want[a.InstanceID]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

var _ ports.StorageClient = (*StorageClient)(nil)

// ---- operations.Repo ----

// OpsRepo — in-memory реализация kacho-corelib/operations.Repo.
type OpsRepo struct {
	mu  sync.Mutex
	ops map[string]*operations.Operation
}

// NewOpsRepo создаёт пустой OpsRepo.
func NewOpsRepo() *OpsRepo { return &OpsRepo{ops: make(map[string]*operations.Operation)} }

// Create сохраняет операцию.
func (r *OpsRepo) Create(_ context.Context, op operations.Operation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := op
	r.ops[op.ID] = &cp
	return nil
}

// CreateWithPrincipal сохраняет операцию + principal (operations.Repo iface).
func (r *OpsRepo) CreateWithPrincipal(_ context.Context, op operations.Operation, p operations.Principal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := op
	cp.Principal = p
	r.ops[op.ID] = &cp
	return nil
}

// Get возвращает shallow-копию операции.
func (r *OpsRepo) Get(_ context.Context, id string) (*operations.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return nil, operations.ErrNotFound
	}
	cp := *op
	return &cp, nil
}

// List возвращает операции (для ListOperations — фильтрует по ResourceID).
func (r *OpsRepo) List(_ context.Context, f operations.ListFilter) ([]operations.Operation, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []operations.Operation
	for _, op := range r.ops {
		if f.ResourceID != "" && extractResourceID(op) != f.ResourceID {
			continue
		}
		out = append(out, *op)
	}
	return out, "", nil
}

// MarkDone помечает операцию завершённой с response.
func (r *OpsRepo) MarkDone(_ context.Context, id string, resp *anypb.Any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.Response = resp
	return nil
}

// MarkError помечает операцию завершённой с ошибкой.
func (r *OpsRepo) MarkError(_ context.Context, id string, errStatus *status.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.Error = errStatus
	return nil
}

// Cancel помечает операцию завершённой.
func (r *OpsRepo) Cancel(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	return nil
}

// ---- operations.OwnedOperationRepo (ownership-scoped Get/Cancel) ----
//
// Зеркалит SQL-предикат pgRepo: доступ только владельцу (пара principal
// type/id); чужой/несуществующий id → ErrNotFound (no-leak).

func opsOwnerMatches(op *operations.Operation, owner operations.Owner) bool {
	return op.Principal.Type == owner.PrincipalType && op.Principal.ID == owner.PrincipalID
}

// GetOwned возвращает операцию только если она принадлежит owner.
func (r *OpsRepo) GetOwned(_ context.Context, id string, owner operations.Owner) (*operations.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok || !opsOwnerMatches(op, owner) {
		return nil, operations.ErrNotFound
	}
	cp := *op
	return &cp, nil
}

// CancelOwned атомарно отменяет операцию owner'а; терминальное состояние в
// возврате (без reload-Get). Идемпотентно на уже-CANCELLED.
func (r *OpsRepo) CancelOwned(_ context.Context, id string, owner operations.Owner) (*operations.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok || !opsOwnerMatches(op, owner) {
		return nil, operations.ErrNotFound
	}
	if op.Done {
		if op.Error != nil && op.Error.GetCode() == 1 {
			cp := *op
			return &cp, nil // идемпотентно: уже CANCELLED
		}
		return nil, operations.ErrAlreadyDone // terminal SUCCESS/ERROR
	}
	op.Done = true
	op.Error = &status.Status{Code: 1, Message: "operation cancelled"}
	cp := *op
	return &cp, nil
}

var _ operations.OwnedOperationRepo = (*OpsRepo)(nil)

// extractResourceID — best-effort извлечение resource_id из metadata
// (для фильтра List). portmock хранит metadata как *anypb.Any; нам достаточно
// сопоставить через operations.MetadataFor — но это требует знания типа. В
// тестах ListOperations проверяет только что список непуст, поэтому здесь
// возвращаем "" (фильтр не применяется) — допустимое упрощение mock'а.
func extractResourceID(_ *operations.Operation) string { return "" }

// ---- await-helpers для async Operation worker'ов ----

// TestingT — минимальный интерфейс из *testing.T/*testing.B для await-helper'ов.
type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// AwaitOpDone детерминированно ждёт завершения worker-горутины (Operation.Done).
// Заменяет фиксированный time.Sleep. Падает через 2s.
func AwaitOpDone(t TestingT, r *OpsRepo, opID string) *operations.Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		op, err := r.Get(context.Background(), opID)
		if err == nil && op.Done {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s did not finish within 2s", opID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// AwaitAllOpsDone ждёт пока все ops в repo станут Done. Падает через 2s.
func AwaitAllOpsDone(t TestingT, r *OpsRepo) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		allDone := true
		var stuckID string
		for id, op := range r.ops {
			if !op.Done {
				allDone = false
				stuckID = id
				break
			}
		}
		r.mu.Unlock()
		if allDone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s did not finish within 2s", stuckID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Compile-time проверки соответствия port-интерфейсам.
var (
	_ ports.DiskRepo      = (*DiskRepo)(nil)
	_ ports.ImageRepo     = (*ImageRepo)(nil)
	_ ports.SnapshotRepo  = (*SnapshotRepo)(nil)
	_ ports.InstanceRepo  = (*InstanceRepo)(nil)
	_ ports.DiskTypeRepo  = (*DiskTypeRepo)(nil)
	_ ports.ZoneRegistry  = (*ZoneRegistry)(nil)
	_ ports.ProjectClient = (*ProjectClient)(nil)
	_ operations.Repo     = (*OpsRepo)(nil)
)
