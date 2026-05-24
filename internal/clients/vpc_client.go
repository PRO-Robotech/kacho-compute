package clients

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	operationv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
	vpcv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// vpcOpPollInterval / vpcOpPollTimeout — параметры опроса VPC Operation при
// inline-аллокации IP. Control-plane операции (Create/Delete Address) у
// kacho-vpc завершаются за ~1с (нет реального data-plane), поэтому короткий
// timeout достаточен; вложенный poll выполняется внутри compute-операции
// (operations.Run worker), что для control-plane приемлемо.
const (
	vpcOpPollInterval = 50 * time.Millisecond
	vpcOpPollTimeout  = 15 * time.Second
)

// VPCClient реализует service.VPCClient через gRPC к kacho-vpc
// (SubnetService / SecurityGroupService — валидация NIC-spec;
// AddressService + OperationService — IPAM-аллокация реальных IPv4 для
// Instance NIC-ей через создание эфемерных Address-ресурсов;
// InternalAddressService — referrer-tracking адресов: привязка/отвязка
// «кто использует адрес» (type=compute_instance, id=instance id)).
//
// Geography (Region/Zone) — домен kacho-compute (эпик KAC-15): зоны больше НЕ
// проксируются в kacho-vpc; compute читает их из своей таблицы `zones` (см.
// internal/repo/catalog_repo.go, ZoneRepoSource).
//
// Использует ДВА gRPC-conn: публичный (:9090 — Subnet/SG/Address/Operation) и
// internal (:9091 — InternalAddressService, не выставлен на external endpoint).
type VPCClient struct {
	subnets       vpcv1.SubnetServiceClient
	sgs           vpcv1.SecurityGroupServiceClient
	addrs         vpcv1.AddressServiceClient
	nics          vpcv1.NetworkInterfaceServiceClient
	ops           operationv1.OperationServiceClient
	internalAddrs vpcv1.InternalAddressServiceClient
}

// NewVPCClient создаёт VPCClient. conn — публичный gRPC-conn kacho-vpc (:9090);
// internalConn — conn к internal-порту kacho-vpc (:9091, InternalAddressService).
func NewVPCClient(conn, internalConn *grpc.ClientConn) *VPCClient {
	return &VPCClient{
		subnets:       vpcv1.NewSubnetServiceClient(conn),
		sgs:           vpcv1.NewSecurityGroupServiceClient(conn),
		addrs:         vpcv1.NewAddressServiceClient(conn),
		nics:          vpcv1.NewNetworkInterfaceServiceClient(conn),
		ops:           operationv1.NewOperationServiceClient(conn),
		internalAddrs: vpcv1.NewInternalAddressServiceClient(internalConn),
	}
}

// GetSubnet возвращает (info, found, error). found=false при NotFound от VPC.
func (c *VPCClient) GetSubnet(ctx context.Context, subnetID string) (service.SubnetInfo, bool, error) {
	var info service.SubnetInfo
	var found bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		sub, rerr := c.subnets.Get(auth.PropagateOutgoing(ctx), &vpcv1.GetSubnetRequest{SubnetId: subnetID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				found = false
				return nil
			}
			return rerr
		}
		found = true
		info = service.SubnetInfo{ZoneID: sub.GetZoneId(), V4CidrBlocks: sub.GetV4CidrBlocks()}
		return nil
	})
	if err != nil {
		return service.SubnetInfo{}, false, err
	}
	return info, found, nil
}

// SecurityGroupExists — true если SG существует.
func (c *VPCClient) SecurityGroupExists(ctx context.Context, sgID string) (bool, error) {
	var found bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.sgs.Get(auth.PropagateOutgoing(ctx), &vpcv1.GetSecurityGroupRequest{SecurityGroupId: sgID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				found = false
				return nil
			}
			return rerr
		}
		found = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

// CreateInternalAddress создаёт эфемерный internal Address в folder/subnet и
// возвращает выделенный VPC-ом IPv4 (из CIDR подсети) + id Address-ресурса.
func (c *VPCClient) CreateInternalAddress(ctx context.Context, folderID, name, subnetID string) (service.VPCAddress, error) {
	req := &vpcv1.CreateAddressRequest{
		ProjectId: folderID,
		Name:      name,
		AddressSpec: &vpcv1.CreateAddressRequest_InternalIpv4AddressSpec{
			InternalIpv4AddressSpec: &vpcv1.InternalIpv4AddressSpec{
				Scope: &vpcv1.InternalIpv4AddressSpec_SubnetId{SubnetId: subnetID},
			},
		},
	}
	addr, err := c.createAddressAndWait(ctx, req)
	if err != nil {
		return service.VPCAddress{}, err
	}
	ip := addr.GetInternalIpv4Address().GetAddress()
	if ip == "" {
		return service.VPCAddress{}, fmt.Errorf("vpc allocated address %s has empty internal ipv4", addr.GetId())
	}
	return service.VPCAddress{IP: ip, AddressID: addr.GetId()}, nil
}

// CreateExternalAddress создаёт эфемерный external Address в folder/zone и
// возвращает выделенный VPC-ом публичный IPv4 (из AddressPool) + id ресурса.
func (c *VPCClient) CreateExternalAddress(ctx context.Context, folderID, name, zoneID string) (service.VPCAddress, error) {
	req := &vpcv1.CreateAddressRequest{
		ProjectId: folderID,
		Name:      name,
		AddressSpec: &vpcv1.CreateAddressRequest_ExternalIpv4AddressSpec{
			ExternalIpv4AddressSpec: &vpcv1.ExternalIpv4AddressSpec{ZoneId: zoneID},
		},
	}
	addr, err := c.createAddressAndWait(ctx, req)
	if err != nil {
		return service.VPCAddress{}, err
	}
	ip := addr.GetExternalIpv4Address().GetAddress()
	if ip == "" {
		return service.VPCAddress{}, fmt.Errorf("vpc allocated address %s has empty external ipv4", addr.GetId())
	}
	return service.VPCAddress{IP: ip, AddressID: addr.GetId()}, nil
}

// GetExternalAddress возвращает (addr, found, error) для существующего Address.
func (c *VPCClient) GetExternalAddress(ctx context.Context, addressID string) (service.VPCAddress, bool, error) {
	var out service.VPCAddress
	var found bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		a, rerr := c.addrs.Get(auth.PropagateOutgoing(ctx), &vpcv1.GetAddressRequest{AddressId: addressID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				found = false
				return nil
			}
			return rerr
		}
		found = true
		out = service.VPCAddress{IP: a.GetExternalIpv4Address().GetAddress(), AddressID: a.GetId()}
		return nil
	})
	if err != nil {
		return service.VPCAddress{}, false, err
	}
	return out, found, nil
}

// DeleteAddress удаляет Address-ресурс (поллит Operation; NotFound = успех).
func (c *VPCClient) DeleteAddress(ctx context.Context, addressID string) error {
	var op *operationv1.Operation
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		op, rerr = c.addrs.Delete(auth.PropagateOutgoing(ctx), &vpcv1.DeleteAddressRequest{AddressId: addressID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				op = nil
				return nil
			}
			return rerr
		}
		return nil
	})
	if err != nil {
		return err
	}
	if op == nil {
		return nil // уже удалён
	}
	if _, err := c.waitOperation(ctx, op); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return nil
		}
		return err
	}
	return nil
}

// SetAddressReference привязывает referrer к VPC Address-ресурсу (кто его
// использует). Идемпотентно. gRPC NotFound (адрес исчез на стороне VPC) →
// возвращается обёрнутая ошибка; вызывающий код в instance.go трактует это
// best-effort (warn + continue — IP уже выделен).
func (c *VPCClient) SetAddressReference(ctx context.Context, addressID, referrerType, referrerID, referrerName string) error {
	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.internalAddrs.SetAddressReference(auth.PropagateOutgoing(ctx), &vpcv1.SetAddressReferenceRequest{
			AddressId:    addressID,
			ReferrerType: referrerType,
			ReferrerId:   referrerID,
			ReferrerName: referrerName,
		})
		if rerr != nil {
			return fmt.Errorf("set address reference %s: %w", addressID, rerr)
		}
		return nil
	})
}

// ClearAddressReference снимает referrer с VPC Address-ресурса. gRPC NotFound
// (адрес уже удалён) → трактуется как успех (нечего снимать).
func (c *VPCClient) ClearAddressReference(ctx context.Context, addressID string) error {
	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.internalAddrs.ClearAddressReference(auth.PropagateOutgoing(ctx), &vpcv1.ClearAddressReferenceRequest{AddressId: addressID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				return nil
			}
			return fmt.Errorf("clear address reference %s: %w", addressID, rerr)
		}
		return nil
	})
}

// MarkAddressEphemeralInUse атомарно (в одной tx на стороне kacho-vpc) помечает
// Address-ресурс как «эфемерный, в работе»: выставляет reserved=false, used=true
// и upsert-ит referrer (kто его использует — type=compute_instance, id/name
// инстанса). Используется для эфемерных NIC/NAT-адресов, которые compute создаёт
// сам через AddressService.Create (а не для reserved пользовательских адресов —
// у тех reserved не трогаем, см. SetAddressReference). gRPC NotFound (адрес исчез
// на стороне VPC) → обёрнутая ошибка; вызывающий код в instance.go трактует это
// best-effort (warn + continue — IP уже выделен).
func (c *VPCClient) MarkAddressEphemeralInUse(ctx context.Context, addressID, referrerType, referrerID, referrerName string) error {
	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.internalAddrs.MarkAddressEphemeralInUse(auth.PropagateOutgoing(ctx), &vpcv1.MarkAddressEphemeralInUseRequest{
			AddressId:    addressID,
			ReferrerType: referrerType,
			ReferrerId:   referrerID,
			ReferrerName: referrerName,
		})
		if rerr != nil {
			return fmt.Errorf("mark address ephemeral-in-use %s: %w", addressID, rerr)
		}
		return nil
	})
}

// CreateNetworkInterface создаёт kacho-vpc NetworkInterface-ресурс
// (NetworkInterfaceService.Create), поллит Operation и возвращает id созданного
// NIC (из Operation metadata `network_interface_id`).
func (c *VPCClient) CreateNetworkInterface(ctx context.Context, req service.CreateNICReq) (string, error) {
	var op *operationv1.Operation
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		op, rerr = c.nics.Create(auth.PropagateOutgoing(ctx), &vpcv1.CreateNetworkInterfaceRequest{
			ProjectId:        req.ProjectID,
			Name:             req.Name,
			SubnetId:         req.SubnetID,
			SecurityGroupIds: req.SecurityGroupIDs,
			V4AddressIds:     req.V4AddressIDs,
			InstanceId:       req.InstanceID,
			Index:            req.Index,
		})
		return rerr
	}); err != nil {
		return "", err
	}
	// The created NIC id is in the operation metadata (CreateNetworkInterfaceMetadata).
	nicID := networkInterfaceIDFromMetadata(op.GetMetadata())
	if _, err := c.waitOperation(ctx, op); err != nil {
		return "", err
	}
	if nicID == "" {
		return "", fmt.Errorf("vpc create network interface operation %s returned no network_interface_id", op.GetId())
	}
	return nicID, nil
}

// GetNetworkInterface возвращает (info, found, error) для существующего NIC.
func (c *VPCClient) GetNetworkInterface(ctx context.Context, nicID string) (service.NICInfo, bool, error) {
	var info service.NICInfo
	var found bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		ni, rerr := c.nics.Get(auth.PropagateOutgoing(ctx), &vpcv1.GetNetworkInterfaceRequest{NetworkInterfaceId: nicID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				found = false
				return nil
			}
			return rerr
		}
		found = true
		info = service.NICInfo{
			ID:               ni.GetId(),
			SubnetID:         ni.GetSubnetId(),
			V4AddressIDs:     ni.GetV4AddressIds(),
			SecurityGroupIDs: ni.GetSecurityGroupIds(),
			// InstanceID — из used_by (referrer.id), который kacho-vpc выставляет
			// на AttachToInstance (raw NIC-ресурс больше не несёт instance_id).
			InstanceID: ni.GetUsedBy().GetReferrer().GetId(),
			Status:     ni.GetStatus().String(),
		}
		return nil
	})
	if err != nil {
		return service.NICInfo{}, false, err
	}
	return info, found, nil
}

// AttachNetworkInterface привязывает NIC к инстансу (поллит Operation).
func (c *VPCClient) AttachNetworkInterface(ctx context.Context, nicID, instanceID, index string) error {
	return c.runNICOp(ctx, func(ctx context.Context) (*operationv1.Operation, error) {
		return c.nics.AttachToInstance(auth.PropagateOutgoing(ctx), &vpcv1.AttachNetworkInterfaceRequest{
			NetworkInterfaceId: nicID, InstanceId: instanceID, Index: index,
		})
	})
}

// DetachNetworkInterface отвязывает NIC от инстанса (best-effort; NotFound = успех).
func (c *VPCClient) DetachNetworkInterface(ctx context.Context, nicID string) error {
	return c.runNICOpTolerant(ctx, func(ctx context.Context) (*operationv1.Operation, error) {
		return c.nics.DetachFromInstance(auth.PropagateOutgoing(ctx), &vpcv1.DetachNetworkInterfaceRequest{NetworkInterfaceId: nicID})
	})
}

// DeleteNetworkInterface удаляет NIC-ресурс (best-effort; NotFound = успех).
func (c *VPCClient) DeleteNetworkInterface(ctx context.Context, nicID string) error {
	return c.runNICOpTolerant(ctx, func(ctx context.Context) (*operationv1.Operation, error) {
		return c.nics.Delete(auth.PropagateOutgoing(ctx), &vpcv1.DeleteNetworkInterfaceRequest{NetworkInterfaceId: nicID})
	})
}

// runNICOp вызывает NIC-RPC, поллит Operation. NotFound на вызове — ошибка.
func (c *VPCClient) runNICOp(ctx context.Context, call func(context.Context) (*operationv1.Operation, error)) error {
	var op *operationv1.Operation
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		op, rerr = call(ctx)
		return rerr
	}); err != nil {
		return err
	}
	_, err := c.waitOperation(ctx, op)
	return err
}

// runNICOpTolerant — как runNICOp, но NotFound (на вызове или при ожидании
// Operation) трактуется как успех (ресурс уже отвязан/удалён).
func (c *VPCClient) runNICOpTolerant(ctx context.Context, call func(context.Context) (*operationv1.Operation, error)) error {
	var op *operationv1.Operation
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		op, rerr = call(ctx)
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				op = nil
				return nil
			}
			return rerr
		}
		return nil
	}); err != nil {
		return err
	}
	if op == nil {
		return nil
	}
	if _, err := c.waitOperation(ctx, op); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return nil
		}
		return err
	}
	return nil
}

// networkInterfaceIDFromMetadata извлекает network_interface_id из
// CreateNetworkInterfaceMetadata (Operation.metadata). "" если не получилось.
func networkInterfaceIDFromMetadata(meta *anypb.Any) string {
	if meta == nil {
		return ""
	}
	m := &vpcv1.CreateNetworkInterfaceMetadata{}
	if err := meta.UnmarshalTo(m); err != nil {
		return ""
	}
	return m.GetNetworkInterfaceId()
}

// createAddressAndWait вызывает AddressService.Create, поллит Operation до
// завершения и читает созданный Address (Operation.response — Address).
func (c *VPCClient) createAddressAndWait(ctx context.Context, req *vpcv1.CreateAddressRequest) (*vpcv1.Address, error) {
	var op *operationv1.Operation
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		op, rerr = c.addrs.Create(auth.PropagateOutgoing(ctx), req)
		return rerr
	}); err != nil {
		return nil, err
	}
	resp, err := c.waitOperation(ctx, op)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("vpc create address operation %s returned no response", op.GetId())
	}
	addr := &vpcv1.Address{}
	if err := resp.UnmarshalTo(addr); err != nil {
		return nil, fmt.Errorf("vpc create address: unmarshal operation response: %w", err)
	}
	return addr, nil
}

// waitOperation поллит OperationService.Get до done=true. Возвращает
// Operation.response (*anypb.Any) либо ошибку (Operation.error смаппленную в
// gRPC-status, или таймаут).
func (c *VPCClient) waitOperation(ctx context.Context, op *operationv1.Operation) (*anypb.Any, error) {
	if op.GetDone() {
		return operationResult(op)
	}
	deadline := time.Now().Add(vpcOpPollTimeout)
	ticker := time.NewTicker(vpcOpPollInterval)
	defer ticker.Stop()
	id := op.GetId()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		var got *operationv1.Operation
		if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
			var rerr error
			got, rerr = c.ops.Get(auth.PropagateOutgoing(ctx), &operationv1.GetOperationRequest{OperationId: id})
			return rerr
		}); err != nil {
			return nil, err
		}
		if got.GetDone() {
			return operationResult(got)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("vpc operation %s did not finish within %s", id, vpcOpPollTimeout)
		}
	}
}

// operationResult извлекает результат завершённой VPC-операции:
// ошибку преобразует в gRPC-статус, иначе возвращает payload ответа.
func operationResult(op *operationv1.Operation) (*anypb.Any, error) {
	if e := op.GetError(); e != nil {
		return nil, status.ErrorProto(e)
	}
	return op.GetResponse(), nil
}

// NoopVPCClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true
// (subnet/SG-проверки → ok, IPAM-аллокация не выполняется — instance.go
// возвращает синтетические IP) и для unit/newman без поднятого kacho-vpc.
type NoopVPCClient struct{}

// GetSubnet возвращает (zero, true, nil) — subnet считается существующим,
// zone-проверка и manual-IP-валидация пропускаются (нет CIDR-блоков).
func (NoopVPCClient) GetSubnet(_ context.Context, _ string) (service.SubnetInfo, bool, error) {
	return service.SubnetInfo{}, true, nil
}

// SecurityGroupExists всегда возвращает (true, nil).
func (NoopVPCClient) SecurityGroupExists(_ context.Context, _ string) (bool, error) { return true, nil }

// CreateInternalAddress возвращает ошибку — в SKIP_PEER_VALIDATION-режиме
// instance.go не должен вызывать IPAM (использует синтетические IP).
func (NoopVPCClient) CreateInternalAddress(_ context.Context, _, _, _ string) (service.VPCAddress, error) {
	return service.VPCAddress{}, fmt.Errorf("vpc IPAM disabled (SKIP_PEER_VALIDATION)")
}

// CreateExternalAddress — см. CreateInternalAddress.
func (NoopVPCClient) CreateExternalAddress(_ context.Context, _, _, _ string) (service.VPCAddress, error) {
	return service.VPCAddress{}, fmt.Errorf("vpc IPAM disabled (SKIP_PEER_VALIDATION)")
}

// GetExternalAddress возвращает (zero, true, nil) — Address считается существующим.
func (NoopVPCClient) GetExternalAddress(_ context.Context, addressID string) (service.VPCAddress, bool, error) {
	return service.VPCAddress{AddressID: addressID}, true, nil
}

// DeleteAddress — no-op.
func (NoopVPCClient) DeleteAddress(_ context.Context, _ string) error { return nil }

// SetAddressReference — no-op (referrer-tracking disabled in SKIP_PEER_VALIDATION).
func (NoopVPCClient) SetAddressReference(_ context.Context, _, _, _, _ string) error { return nil }

// ClearAddressReference — no-op.
func (NoopVPCClient) ClearAddressReference(_ context.Context, _ string) error { return nil }

// MarkAddressEphemeralInUse — no-op (referrer-tracking disabled in SKIP_PEER_VALIDATION).
func (NoopVPCClient) MarkAddressEphemeralInUse(_ context.Context, _, _, _, _ string) error {
	return nil
}

// CreateNetworkInterface возвращает ошибку — в SKIP_PEER_VALIDATION-режиме
// instance.go не создаёт kacho-vpc NIC (синтетический NIC без vpc-ресурса).
func (NoopVPCClient) CreateNetworkInterface(_ context.Context, _ service.CreateNICReq) (string, error) {
	return "", fmt.Errorf("vpc network interface management disabled (SKIP_PEER_VALIDATION)")
}

// GetNetworkInterface возвращает (zero, true, nil) — NIC считается существующим.
func (NoopVPCClient) GetNetworkInterface(_ context.Context, nicID string) (service.NICInfo, bool, error) {
	return service.NICInfo{ID: nicID}, true, nil
}

// AttachNetworkInterface — no-op.
func (NoopVPCClient) AttachNetworkInterface(_ context.Context, _, _, _ string) error { return nil }

// DetachNetworkInterface — no-op.
func (NoopVPCClient) DetachNetworkInterface(_ context.Context, _ string) error { return nil }

// DeleteNetworkInterface — no-op.
func (NoopVPCClient) DeleteNetworkInterface(_ context.Context, _ string) error { return nil }
