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
	vpcv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// defaultNicCallTimeout — per-call deadline на КАЖДЫЙ compute→vpc
// InternalNetworkInterfaceService вызов (architecture.md: per-call deadline на
// каждом внешнем вызове). Без него app-slow vpc (peer жив, но не отвечает) висит
// до inbound ctx (LRO worker opTimeout) и копит worker-слоты. Пакетный дефолт-const
// (нет конфиг-кноба на это ребро) — как defaultZoneCallTimeout для geo-ребра.
const defaultNicCallTimeout = 5 * time.Second

// VPCNicClient реализует ports.NicClient поверх gRPC к kacho-vpc
// InternalNetworkInterfaceService (internal :9091, mTLS). Владелец NIC↔Instance
// привязки — kacho-vpc: Attach/Detach делают атомарный used_by_id-CAS на vpc-строке
// NIC с zone-coherence; compute лишь форвардит self-describing payload (vpc НЕ зовёт
// compute — ацикличность).
//
// Каждая попытка несёт собственный context.WithTimeout(c.timeout);
// retry.OnUnavailable сглаживает транзиентные обрывы, а outgoing ctx обёрнут
// auth.PropagateOutgoing, чтобы vpc-side per-RPC authz-Check видел реального
// caller'а (security.md). Ошибки vpc пробрасываются как есть — use-case (mapNicErr)
// нормализует контрактные коды и чистит транспортную недоступность в Unavailable.
type VPCNicClient struct {
	cli     vpcv1.InternalNetworkInterfaceServiceClient
	timeout time.Duration
}

// NewVPCNicClient создаёт VPCNicClient поверх gRPC-conn к kacho-vpc (:9091 internal).
func NewVPCNicClient(conn *grpc.ClientConn) *VPCNicClient {
	return &VPCNicClient{cli: vpcv1.NewInternalNetworkInterfaceServiceClient(conn), timeout: defaultNicCallTimeout}
}

// NewVPCNicClientWith — seam для unit-тестов (готовый клиент-стаб).
func NewVPCNicClientWith(cli vpcv1.InternalNetworkInterfaceServiceClient) *VPCNicClient {
	return &VPCNicClient{cli: cli, timeout: defaultNicCallTimeout}
}

// Attach форвардит self-describing attach-payload в vpc (атомарный CAS + zone-
// coherence на стороне vpc). Идемпотентен на replay (already-ours → OK на стороне vpc).
func (c *VPCNicClient) Attach(ctx context.Context, spec ports.NicAttachSpec) (*ports.NicAttachment, error) {
	var out *ports.NicAttachment
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		resp, rerr := c.cli.Attach(auth.PropagateOutgoing(callCtx), &vpcv1.AttachNetworkInterfaceRequest{
			NicId:          spec.NICID,
			InstanceId:     spec.InstanceID,
			InstanceName:   spec.InstanceName,
			InstanceZoneId: spec.InstanceZoneID,
			ProjectId:      spec.ProjectID,
			Index:          spec.Index,
		})
		if rerr != nil {
			return rerr
		}
		out = attachmentFromNIC(spec.InstanceID, resp.GetNetworkInterface())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Detach снимает NIC↔Instance-привязку (идемпотентно на стороне vpc).
func (c *VPCNicClient) Detach(ctx context.Context, nicID, instanceID string) error {
	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		_, rerr := c.cli.Detach(auth.PropagateOutgoing(callCtx), &vpcv1.DetachNetworkInterfaceRequest{
			NicId:      nicID,
			InstanceId: instanceID,
		})
		return rerr
	})
}

// ListByInstance — batched read NIC-привязок для Instance.Get/List зеркала.
func (c *VPCNicClient) ListByInstance(ctx context.Context, instanceIDs []string) ([]ports.NicAttachment, error) {
	var out []ports.NicAttachment
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		resp, rerr := c.cli.ListByInstance(auth.PropagateOutgoing(callCtx), &vpcv1.ListNetworkInterfacesByInstanceRequest{
			InstanceIds: instanceIDs,
		})
		if rerr != nil {
			return rerr
		}
		out = out[:0]
		for _, a := range resp.GetNetworkInterfaces() {
			out = append(out, ports.NicAttachment{
				NICID:            a.GetNicId(),
				InstanceID:       a.GetInstanceId(),
				Index:            a.GetIndex(),
				SubnetID:         a.GetSubnetId(),
				PrimaryV4Address: a.GetPrimaryV4Address(),
				PrimaryV6Address: a.GetPrimaryV6Address(),
				SecurityGroupIDs: a.GetSecurityGroupIds(),
				MACAddress:       a.GetMacAddress(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// attachmentFromNIC мапит публичный vpc NetworkInterface (Attach-ответ) в
// ports.NicAttachment (id/subnet/mac/sg). Публичное NIC-сообщение НЕ несёт slot
// index и резолвнутые primary-адреса (только *_address_ids) — авторитетное зеркало
// со слотами и адресами строит ListByInstance; здесь эти поля не заполняются.
func attachmentFromNIC(instanceID string, ni *vpcv1.NetworkInterface) *ports.NicAttachment {
	if ni == nil {
		return &ports.NicAttachment{InstanceID: instanceID}
	}
	return &ports.NicAttachment{
		NICID:            ni.GetId(),
		InstanceID:       instanceID,
		SubnetID:         ni.GetSubnetId(),
		SecurityGroupIDs: ni.GetSecurityGroupIds(),
		MACAddress:       ni.GetMacAddress(),
	}
}

// NoopNicClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION / когда vpc-ребро
// не сконфигурировано. Attach/Detach fail-closed Unavailable (NIC-привязка требует
// живого kacho-vpc — синтетики нет), ListByInstance пуст (зеркало опускается).
type NoopNicClient struct{}

// Attach всегда Unavailable — привязка невозможна без kacho-vpc.
func (NoopNicClient) Attach(_ context.Context, _ ports.NicAttachSpec) (*ports.NicAttachment, error) {
	return nil, status.Error(codes.Unavailable, "network interface service not configured")
}

// Detach всегда Unavailable.
func (NoopNicClient) Detach(_ context.Context, _, _ string) error {
	return status.Error(codes.Unavailable, "network interface service not configured")
}

// ListByInstance всегда пуст (зеркало грациозно опускается).
func (NoopNicClient) ListByInstance(_ context.Context, _ []string) ([]ports.NicAttachment, error) {
	return nil, nil
}

var (
	_ ports.NicClient = (*VPCNicClient)(nil)
	_ ports.NicClient = NoopNicClient{}
)
