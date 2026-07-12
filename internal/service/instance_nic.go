// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"sort"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// niResource — human-label для malformed-id ошибок NIC формат kacho-vpc
// `corevalidate.ResourceID`: `invalid network interface id <X>`).
const niResource = "network interface"

// AttachNetworkInterface привязывает существующий kacho-vpc NIC к инстансу (S4).
//
// Владелец привязки — kacho-vpc (NIC first-class): compute держит НОЛЬ local
// attach-state; вся мутация — атомарный used_by_id-CAS на vpc-строке NIC с
// zone-coherence (anycast/REGIONAL-subnet исключён). compute лишь (1) проверяет
// malformed nic-id синхронно первым стейтментом, (2) в worker'е читает инстанс
// (state-гейт RUNNING/STOPPED + self-describing zone/name/project) и (3) форвардит
// self-describing payload в vpc (vpc НЕ зовёт compute — ацикличность). Несколько
// NIC на инстанс; vpc назначает первый свободный слот при index==0.
func (s *InstanceService) AttachNetworkInterface(ctx context.Context, instanceID, nicID string, index int32) (*operations.Operation, error) {
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	// Malformed enp-id — ПЕРВЫМ стейтментом (api-conventions): sync InvalidArgument
	// "invalid network interface id '<X>'" (тот же текст, что kacho-vpc). Без format-check
	// невалидный id ушёл бы в vpc и вернул generic ошибку.
	if err := corevalidate.ResourceID(niResource, ids.PrefixNetworkInterface, nicID); err != nil {
		return nil, err
	}
	return runOp(ctx, s.opsRepo, "Attach network interface to instance "+instanceID,
		&computev1.AttachInstanceNetworkInterfaceMetadata{InstanceId: instanceID, NicId: nicID},
		func(ctx context.Context) (*anypb.Any, error) {
			in, err := s.repo.Get(ctx, instanceID)
			if err != nil {
				return nil, mapRepoErr(err)
			}
			if in.Status != domain.InstanceStatusRunning && in.Status != domain.InstanceStatusStopped {
				return nil, status.Error(codes.FailedPrecondition, "Instance is not running or stopped")
			}
			if s.nicClient == nil {
				return nil, status.Error(codes.Unavailable, "network interface service unavailable")
			}
			if _, err := s.nicClient.Attach(ctx, NicAttachSpec{
				NICID:          nicID,
				InstanceID:     in.ID,
				InstanceName:   in.Name,
				InstanceZoneID: in.ZoneID,
				ProjectID:      in.ProjectID,
				Index:          index,
			}); err != nil {
				return nil, mapNicErr(err)
			}
			s.applyNicMirror(ctx, in)
			return anypb.New(protoconv.Instance(in))
		})
}

// DetachNetworkInterface отвязывает NIC от инстанса по nic_id ЛИБО по slot index
// (oneof — обе ветки эквивалентны по эффекту). Идемпотентно: уже-отвязанный
// NIC / отсутствующий слот → done no-op. NIC-ресурс (kacho-vpc) не удаляется —
// снимается только привязка.
func (s *InstanceService) DetachNetworkInterface(ctx context.Context, instanceID, nicID string, index int32, byIndex bool) (*operations.Operation, error) {
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id required")
	}
	if !byIndex {
		if err := corevalidate.ResourceID(niResource, ids.PrefixNetworkInterface, nicID); err != nil {
			return nil, err
		}
	}
	return runOp(ctx, s.opsRepo, "Detach network interface from instance "+instanceID,
		&computev1.DetachInstanceNetworkInterfaceMetadata{InstanceId: instanceID, NicId: nicID},
		func(ctx context.Context) (*anypb.Any, error) {
			in, err := s.repo.Get(ctx, instanceID)
			if err != nil {
				return nil, mapRepoErr(err)
			}
			if s.nicClient == nil {
				return nil, status.Error(codes.Unavailable, "network interface service unavailable")
			}
			target := nicID
			if byIndex {
				// Резолвим nic_id для слота через batched read (source of truth = vpc).
				atts, err := s.nicClient.ListByInstance(ctx, []string{instanceID})
				if err != nil {
					return nil, mapNicErr(err)
				}
				target = ""
				for i := range atts {
					if atts[i].Index == index {
						target = atts[i].NICID
						break
					}
				}
				if target == "" {
					// Нет NIC на этом слоте — идемпотентный no-op.
					s.applyNicMirror(ctx, in)
					return anypb.New(protoconv.Instance(in))
				}
			}
			if err := s.nicClient.Detach(ctx, target, instanceID); err != nil {
				return nil, mapNicErr(err)
			}
			s.applyNicMirror(ctx, in)
			return anypb.New(protoconv.Instance(in))
		})
}

// applyNicMirror заполняет in.NetworkInterfaces read-only зеркалом из kacho-vpc
// (source of truth). Graceful-degrade: nil-client или ошибка vpc → зеркало
// опускается (не трогаем in), Get/List не падают (consumer грациозно переживает недоступность owner'а).
func (s *InstanceService) applyNicMirror(ctx context.Context, in *domain.Instance) {
	if s.nicClient == nil || in == nil {
		return
	}
	atts, err := s.nicClient.ListByInstance(ctx, []string{in.ID})
	if err != nil {
		return
	}
	in.NetworkInterfaces = nicMirror(atts)
}

// applyNicMirrorBatch — batched (не N+1) зеркало для List: один ListByInstance по
// всем id, затем раскладка по инстансам. Graceful-degrade как applyNicMirror.
func (s *InstanceService) applyNicMirrorBatch(ctx context.Context, list []*domain.Instance) {
	if s.nicClient == nil || len(list) == 0 {
		return
	}
	ids := make([]string, 0, len(list))
	for _, in := range list {
		ids = append(ids, in.ID)
	}
	atts, err := s.nicClient.ListByInstance(ctx, ids)
	if err != nil {
		return
	}
	byInstance := make(map[string][]NicAttachment, len(list))
	for _, a := range atts {
		byInstance[a.InstanceID] = append(byInstance[a.InstanceID], a)
	}
	for _, in := range list {
		in.NetworkInterfaces = nicMirror(byInstance[in.ID])
	}
}

// nicMirror конвертирует vpc-attachments в domain.NetworkInterface (read-only
// denormalised mirror), отсортированные по slot index для детерминизма.
func nicMirror(atts []NicAttachment) []domain.NetworkInterface {
	if len(atts) == 0 {
		return nil
	}
	sorted := make([]NicAttachment, len(atts))
	copy(sorted, atts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Index < sorted[j].Index })
	out := make([]domain.NetworkInterface, 0, len(sorted))
	for i := range sorted {
		a := &sorted[i]
		out = append(out, domain.NetworkInterface{
			Index:            strconv.Itoa(int(a.Index)),
			NICID:            a.NICID,
			MACAddress:       a.MACAddress,
			SubnetID:         a.SubnetID,
			PrimaryV4Address: a.PrimaryV4Address,
			PrimaryV6Address: a.PrimaryV6Address,
			SecurityGroupIDs: a.SecurityGroupIDs,
		})
	}
	return out
}

// mapNicErr нормализует ошибку compute→vpc NIC-ребра по whitelist-контракту
// (leak-guard, security.md инвариант N1):
//   - vpc-контрактные коды (FailedPrecondition "NetworkInterface is in use" /
//     zone-coherence, InvalidArgument, NotFound, AlreadyExists, PermissionDenied) —
//     пробрасываются дословно (часть контракта S4-03/04, не transport-leak);
//   - транспортная недоступность / DeadlineExceeded / не-status ошибка → чистый
//     opaque Unavailable (fail-closed для мутации, без leak dial-деталей);
//   - ВСЁ прочее (Internal / Unknown / …) — НЕ форвардим дословно: un-sentineled
//     Internal может нести pgx/driver/connection-детали vpc (host/port/db) →
//     фиксированный opaque codes.Internal "internal error".
func mapNicErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return status.Error(codes.Unavailable, "network interface service unavailable")
	}
	switch st.Code() {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound,
		codes.AlreadyExists, codes.PermissionDenied:
		return status.Error(st.Code(), st.Message())
	case codes.Unavailable, codes.DeadlineExceeded:
		return status.Error(codes.Unavailable, "network interface service unavailable")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
