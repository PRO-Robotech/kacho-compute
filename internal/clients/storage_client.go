// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package clients

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	storagev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/storage/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// defaultVolumeCallTimeout — per-call deadline на КАЖДЫЙ compute→storage
// InternalVolumeService вызов (architecture.md: per-call deadline на каждом внешнем
// вызове). Без него app-slow storage (peer жив, но не отвечает) висит до inbound ctx
// (LRO worker opTimeout) и копит worker-слоты. Пакетный дефолт-const (нет конфиг-кноба
// на это ребро) — как defaultNicCallTimeout для vpc-ребра.
const defaultVolumeCallTimeout = 5 * time.Second

// StorageClient реализует ports.StorageClient поверх gRPC к kacho-storage
// InternalVolumeService (internal :9091, mTLS). Владелец volume↔Instance-привязки —
// kacho-storage: Attach/Detach делают атомарный attach-CAS на storage-строке
// volume_attachments с zone/project-coherence; compute лишь форвардит self-describing
// payload (storage НЕ зовёт compute — ацикличность; attach-state живёт на storage-стороне,
// compute локальной attach-строки не держит).
//
// Каждая попытка несёт собственный context.WithTimeout(c.timeout);
// retry.OnUnavailable сглаживает транзиентные обрывы, а outgoing ctx обёрнут
// auth.PropagateOutgoing, чтобы storage-side per-RPC authz-Check видел реального
// caller'а (security.md). Транспортная недоступность (Unavailable/DeadlineExceeded)
// нормализуется в фиксированный opaque Unavailable (leak-guard: сырой dial-текст с
// host/port peer'а НЕ утекает наружу, security.md hardening-инвариант N1); контрактные
// коды storage (InvalidArgument/FailedPrecondition/NotFound + их тексты) пробрасываются
// как есть — это собственный контракт владельца, не transport-leak.
type StorageClient struct {
	cli     storagev1.InternalVolumeServiceClient
	timeout time.Duration
}

// NewStorageClient создаёт StorageClient поверх gRPC-conn к kacho-storage (:9091 internal).
func NewStorageClient(conn *grpc.ClientConn) *StorageClient {
	return &StorageClient{cli: storagev1.NewInternalVolumeServiceClient(conn), timeout: defaultVolumeCallTimeout}
}

// NewStorageClientWith — seam для unit-тестов (готовый клиент-стаб).
func NewStorageClientWith(cli storagev1.InternalVolumeServiceClient) *StorageClient {
	return &StorageClient{cli: cli, timeout: defaultVolumeCallTimeout}
}

// Attach форвардит self-describing attach-payload в storage (атомарный attach-CAS +
// zone/project-coherence на стороне storage). Идемпотентен на replay (already-ours →
// OK на стороне storage). Из ответного Volume (status IN_USE) вычленяется привязка
// именно для этого instance и мапится в ports.VolumeAttachmentInfo (с volume_id из
// Volume) — compute-side зеркало не носит полный Volume.
func (c *StorageClient) Attach(ctx context.Context, spec ports.VolumeAttachSpec) (*ports.VolumeAttachmentInfo, error) {
	var out *ports.VolumeAttachmentInfo
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		resp, rerr := c.cli.Attach(auth.PropagateOutgoing(callCtx), &storagev1.AttachVolumeRequest{
			VolumeId:       spec.VolumeID,
			InstanceId:     spec.InstanceID,
			InstanceName:   spec.InstanceName,
			InstanceZoneId: spec.InstanceZoneID,
			ProjectId:      spec.ProjectID,
			DeviceName:     spec.DeviceName,
			IsBoot:         spec.IsBoot,
			Mode:           attachModeToWire(spec.Mode),
			AutoDelete:     spec.AutoDelete,
		})
		if rerr != nil {
			return rerr
		}
		out = attachmentFromVolume(spec.InstanceID, resp.GetVolume())
		return nil
	})
	if err != nil {
		return nil, mapStorageErr(err)
	}
	return out, nil
}

// Detach снимает volume↔Instance-привязку (идемпотентно на стороне storage).
func (c *StorageClient) Detach(ctx context.Context, volumeID, instanceID string) error {
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		_, rerr := c.cli.Detach(auth.PropagateOutgoing(callCtx), &storagev1.DetachVolumeRequest{
			VolumeId:   volumeID,
			InstanceId: instanceID,
		})
		return rerr
	})
	return mapStorageErr(err)
}

// ListAttachments — batched read volume-привязок для Instance.Get/List зеркала.
func (c *StorageClient) ListAttachments(ctx context.Context, instanceIDs []string) ([]ports.VolumeAttachmentInfo, error) {
	var out []ports.VolumeAttachmentInfo
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		resp, rerr := c.cli.ListAttachments(auth.PropagateOutgoing(callCtx), &storagev1.ListAttachmentsRequest{
			InstanceIds: instanceIDs,
		})
		if rerr != nil {
			return rerr
		}
		out = out[:0]
		for _, a := range resp.GetAttachments() {
			out = append(out, ports.VolumeAttachmentInfo{
				VolumeID:     a.GetVolumeId(),
				InstanceID:   a.GetInstanceId(),
				InstanceName: a.GetInstanceName(),
				DeviceName:   a.GetDeviceName(),
				IsBoot:       a.GetIsBoot(),
				Mode:         attachModeFromWire(a.GetMode()),
				AutoDelete:   a.GetAutoDelete(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, mapStorageErr(err)
	}
	return out, nil
}

// attachmentFromVolume вычленяет из Volume (Attach-ответ) привязку именно для
// instanceID и мапит её в ports.VolumeAttachmentInfo (volume_id берётся из самого
// Volume). Если строки для instance нет (не должно происходить при успешном Attach) —
// возвращается минимальный slug с volume_id/instance_id, чтобы вызывающий не паниковал.
func attachmentFromVolume(instanceID string, vol *storagev1.Volume) *ports.VolumeAttachmentInfo {
	info := &ports.VolumeAttachmentInfo{InstanceID: instanceID}
	if vol == nil {
		return info
	}
	info.VolumeID = vol.GetId()
	for _, a := range vol.GetAttachments() {
		if a.GetInstanceId() == instanceID {
			info.InstanceName = a.GetInstanceName()
			info.DeviceName = a.GetDeviceName()
			info.IsBoot = a.GetIsBoot()
			info.Mode = attachModeFromWire(a.GetMode())
			info.AutoDelete = a.GetAutoDelete()
			break
		}
	}
	return info
}

// attachModeToWire мапит нейтральный ports.VolumeAttachMode в wire-enum.
func attachModeToWire(m ports.VolumeAttachMode) storagev1.VolumeAttachment_Mode {
	switch m {
	case ports.VolumeAttachModeReadWrite:
		return storagev1.VolumeAttachment_READ_WRITE
	case ports.VolumeAttachModeReadOnly:
		return storagev1.VolumeAttachment_READ_ONLY
	default:
		return storagev1.VolumeAttachment_MODE_UNSPECIFIED
	}
}

// attachModeFromWire мапит wire-enum обратно в нейтральный ports.VolumeAttachMode.
func attachModeFromWire(m storagev1.VolumeAttachment_Mode) ports.VolumeAttachMode {
	switch m {
	case storagev1.VolumeAttachment_READ_WRITE:
		return ports.VolumeAttachModeReadWrite
	case storagev1.VolumeAttachment_READ_ONLY:
		return ports.VolumeAttachModeReadOnly
	default:
		return ports.VolumeAttachModeUnspecified
	}
}

// mapStorageErr нормализует ошибку компьют→storage-вызова: транспортная
// недоступность (Unavailable / DeadlineExceeded / не-status ошибка) → фиксированный
// opaque Unavailable (fail-closed для мутаций, leak-guard: сырой dial-текст с host/port
// peer'а НЕ утекает наружу). Контрактные коды storage (InvalidArgument /
// FailedPrecondition / NotFound / AlreadyExists) пробрасываются как есть — это
// собственный контракт владельца, не transport-leak.
func mapStorageErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return status.Error(codes.Unavailable, "storage service unavailable")
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return status.Error(codes.Unavailable, "storage service unavailable")
	default:
		return status.Error(st.Code(), st.Message())
	}
}

// NoopStorageClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION / когда
// storage-ребро не сконфигурировано. Attach/Detach fail-closed Unavailable
// (volume-привязка требует живого kacho-storage — синтетики нет), ListAttachments пуст
// (зеркало опускается).
type NoopStorageClient struct{}

// Attach всегда Unavailable — привязка невозможна без kacho-storage.
func (NoopStorageClient) Attach(_ context.Context, _ ports.VolumeAttachSpec) (*ports.VolumeAttachmentInfo, error) {
	return nil, status.Error(codes.Unavailable, "volume service not configured")
}

// Detach всегда Unavailable.
func (NoopStorageClient) Detach(_ context.Context, _, _ string) error {
	return status.Error(codes.Unavailable, "volume service not configured")
}

// ListAttachments всегда пуст (зеркало грациозно опускается).
func (NoopStorageClient) ListAttachments(_ context.Context, _ []string) ([]ports.VolumeAttachmentInfo, error) {
	return nil, nil
}

var (
	_ ports.StorageClient = (*StorageClient)(nil)
	_ ports.StorageClient = NoopStorageClient{}
)
