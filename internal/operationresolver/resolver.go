// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package operationresolver — доменный resolver осиротевших LRO для kacho-compute.
//
// Движок reconciler'а живёт в kacho-corelib/operations (сканирует таблицу
// operations по grace-окну, клеймит orphan'ы под FOR UPDATE SKIP LOCKED). Сам
// resolver — доменная часть в сервисе: он знает типы метаданных compute
// (*computev1.<Verb><Resource>Metadata) и сверяет осиротевшую операцию с
// committed-реальностью ресурса через repo.Get.
//
// Контракт диспетчеризации (writer-TX атомарна, частичных состояний нет):
//   - Create-метаданные: ресурс присутствует → Done(current ресурс как Response);
//     отсутствует → Interrupted.
//   - Update / lifecycle-метаданные (Start/Stop/Restart/Attach/… — существование
//     ресурса не меняют): присутствует → Done(current); отсутствует → Interrupted.
//   - Delete-метаданные: отсутствует → Done(Empty); присутствует → Interrupted.
//   - неузнанный / nil тип метаданных → Skip (строка остаётся done=false, sweep
//     повторится);
//   - transient-ошибка чтения ресурса → (ResolverResult{}, err): движок
//     инкрементит reconcile_errors и пропускает orphan до следующего sweep'а.
//
// Resolver не делает re-drive (повторный запуск worker-fn) — он приводит статус
// операции в соответствие тому, что реально закоммичено.
package operationresolver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// DiskReader / ImageReader / SnapshotReader / InstanceReader — узкие read-порты
// четырёх мутируемых ресурсов compute. Удовлетворяются *repo.DiskRepo и т.д.
type DiskReader interface {
	Get(ctx context.Context, id string) (*domain.Disk, error)
}

type ImageReader interface {
	Get(ctx context.Context, id string) (*domain.Image, error)
}

type SnapshotReader interface {
	Get(ctx context.Context, id string) (*domain.Snapshot, error)
}

type InstanceReader interface {
	Get(ctx context.Context, id string) (*domain.Instance, error)
}

// Readers — набор read-портов, инжектируемый composition root'ом.
type Readers struct {
	Disk     DiskReader
	Image    ImageReader
	Snapshot SnapshotReader
	Instance InstanceReader
}

// kind — категория операции, выводимая из типа метаданных.
type kind int

const (
	kindCreate kind = iota // present → Done(current); absent → Interrupted
	kindUpdate             // как Create (reconcile к committed-реальности, не re-apply)
	kindDelete             // absent → Done(Empty); present → Interrupted
)

// Resolver — доменный resolver compute поверх узких read-портов репозиториев.
type Resolver struct {
	r   Readers
	log *slog.Logger
}

// Option — функциональная опция Resolver.
type Option func(*Resolver)

// WithLogger подключает структурированный логгер (диагностика resolve).
func WithLogger(l *slog.Logger) Option {
	return func(r *Resolver) {
		if l != nil {
			r.log = l
		}
	}
}

// New конструирует Resolver поверх набора read-портов.
func New(r Readers, opts ...Option) *Resolver {
	rs := &Resolver{r: r, log: slog.Default()}
	for _, o := range opts {
		o(rs)
	}
	return rs
}

// Resolve реализует operations.Resolver: по метаданным осиротевшей операции
// определяет терминальный исход, сверяясь с committed-реальностью ресурса.
func (rs *Resolver) Resolve(ctx context.Context, op operations.Operation) (operations.ResolverResult, error) {
	if op.Metadata == nil {
		return skip(), nil
	}
	msg, err := op.Metadata.UnmarshalNew()
	if err != nil {
		// Неизвестный / неразбираемый тип метаданных — не наша операция в этом
		// прогоне. Skip, а не ошибка: строка остаётся done=false.
		rs.log.Warn("operation resolver: undecodable metadata, skipping orphan",
			"op", op.ID, "type_url", op.Metadata.TypeUrl, "err", err)
		return skip(), nil
	}

	switch m := msg.(type) {
	// ---- Disk ----
	case *computev1.CreateDiskMetadata:
		return resolveExistence(ctx, kindCreate, m.GetDiskId(), rs.r.Disk, marshalDisk)
	case *computev1.UpdateDiskMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetDiskId(), rs.r.Disk, marshalDisk)
	case *computev1.RelocateDiskMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetDiskId(), rs.r.Disk, marshalDisk)
	case *computev1.DeleteDiskMetadata:
		return resolveExistence(ctx, kindDelete, m.GetDiskId(), rs.r.Disk, marshalDisk)

	// ---- Image ----
	case *computev1.CreateImageMetadata:
		return resolveExistence(ctx, kindCreate, m.GetImageId(), rs.r.Image, marshalImage)
	case *computev1.UpdateImageMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetImageId(), rs.r.Image, marshalImage)
	case *computev1.DeleteImageMetadata:
		return resolveExistence(ctx, kindDelete, m.GetImageId(), rs.r.Image, marshalImage)

	// ---- Snapshot ----
	case *computev1.CreateSnapshotMetadata:
		return resolveExistence(ctx, kindCreate, m.GetSnapshotId(), rs.r.Snapshot, marshalSnapshot)
	case *computev1.UpdateSnapshotMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetSnapshotId(), rs.r.Snapshot, marshalSnapshot)
	case *computev1.DeleteSnapshotMetadata:
		return resolveExistence(ctx, kindDelete, m.GetSnapshotId(), rs.r.Snapshot, marshalSnapshot)

	// ---- Instance: Create / Update / Delete ----
	case *computev1.CreateInstanceMetadata:
		return resolveExistence(ctx, kindCreate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.UpdateInstanceMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.DeleteInstanceMetadata:
		return resolveExistence(ctx, kindDelete, m.GetInstanceId(), rs.r.Instance, marshalInstance)

	// ---- Instance lifecycle (existence-preserving) → reconcile к current ----
	case *computev1.StartInstanceMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.StopInstanceMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.RestartInstanceMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.AttachInstanceDiskMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.DetachInstanceDiskMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.AddInstanceOneToOneNatMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.RemoveInstanceOneToOneNatMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.UpdateInstanceMetadataMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)
	case *computev1.SimulateInstanceMaintenanceEventMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetInstanceId(), rs.r.Instance, marshalInstance)

	default:
		// Прочие (blocked / unwired) типы метаданных — не наши.
		return skip(), nil
	}
}

// resolveExistence — общая логика «существование ресурса → терминальный исход».
// reader.Get читает ресурс (ports.ErrNotFound → отсутствует), toAny упаковывает
// текущий ресурс в Operation.response для Done на Create/Update. Если reader
// не сконфигурирован (nil — dev/неполный wiring), orphan пропускается (Skip).
func resolveExistence[T any](
	ctx context.Context,
	k kind,
	id string,
	reader interface {
		Get(context.Context, string) (*T, error)
	},
	toAny func(*T) (*anypb.Any, error),
) (operations.ResolverResult, error) {
	if reader == nil {
		return skip(), nil
	}
	rec, err := reader.Get(ctx, id)
	switch {
	case err == nil:
		// present
	case errors.Is(err, ports.ErrNotFound):
		rec = nil // absent
	default:
		// transient read-ошибка → движок инкрементит reconcile_errors, пропускает.
		return operations.ResolverResult{}, fmt.Errorf("operationresolver: get %q: %w", id, err)
	}

	present := rec != nil
	if k == kindDelete {
		if present {
			return interrupted(), nil
		}
		return done(nil), nil // Empty-семантика
	}
	// Create / Update / lifecycle.
	if !present {
		return interrupted(), nil
	}
	resp, err := toAny(rec)
	if err != nil {
		return operations.ResolverResult{}, fmt.Errorf("operationresolver: marshal %q: %w", id, err)
	}
	return done(resp), nil
}

func marshalDisk(d *domain.Disk) (*anypb.Any, error)         { return anypb.New(protoconv.Disk(d)) }
func marshalImage(i *domain.Image) (*anypb.Any, error)       { return anypb.New(protoconv.Image(i)) }
func marshalSnapshot(s *domain.Snapshot) (*anypb.Any, error) { return anypb.New(protoconv.Snapshot(s)) }
func marshalInstance(in *domain.Instance) (*anypb.Any, error) {
	return anypb.New(protoconv.Instance(in))
}

func skip() operations.ResolverResult {
	return operations.ResolverResult{Outcome: operations.OutcomeSkip}
}

func interrupted() operations.ResolverResult {
	return operations.ResolverResult{Outcome: operations.OutcomeInterrupted}
}

func done(resp *anypb.Any) operations.ResolverResult {
	return operations.ResolverResult{Outcome: operations.OutcomeDone, Response: resp}
}
