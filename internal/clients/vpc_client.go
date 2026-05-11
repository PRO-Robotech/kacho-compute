package clients

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	vpcv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"
)

// VPCClient реализует service.VPCClient через gRPC к kacho-vpc
// (SubnetService/SecurityGroupService/AddressService.Get) — для валидации
// Instance network_interface_spec в worker'е Create.
type VPCClient struct {
	subnets vpcv1.SubnetServiceClient
	sgs     vpcv1.SecurityGroupServiceClient
	addrs   vpcv1.AddressServiceClient
}

// NewVPCClient создаёт VPCClient.
func NewVPCClient(conn *grpc.ClientConn) *VPCClient {
	return &VPCClient{
		subnets: vpcv1.NewSubnetServiceClient(conn),
		sgs:     vpcv1.NewSecurityGroupServiceClient(conn),
		addrs:   vpcv1.NewAddressServiceClient(conn),
	}
}

// GetSubnet возвращает (zoneID, found, error). found=false при NotFound от VPC.
func (c *VPCClient) GetSubnet(ctx context.Context, subnetID string) (string, bool, error) {
	var zoneID string
	var found bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		sub, rerr := c.subnets.Get(ctx, &vpcv1.GetSubnetRequest{SubnetId: subnetID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				found = false
				return nil
			}
			return rerr
		}
		found = true
		zoneID = sub.GetZoneId()
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return zoneID, found, nil
}

// SecurityGroupExists — true если SG существует.
func (c *VPCClient) SecurityGroupExists(ctx context.Context, sgID string) (bool, error) {
	var found bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.sgs.Get(ctx, &vpcv1.GetSecurityGroupRequest{SecurityGroupId: sgID})
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

// AddressExists — true если Address существует.
func (c *VPCClient) AddressExists(ctx context.Context, addressID string) (bool, error) {
	var found bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.addrs.Get(ctx, &vpcv1.GetAddressRequest{AddressId: addressID})
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

// NoopVPCClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true
// (все existence-проверки → true) и для unit/newman без поднятого kacho-vpc.
type NoopVPCClient struct{}

// GetSubnet возвращает ("", true, nil) — subnet считается существующим, zone-проверка пропускается.
func (NoopVPCClient) GetSubnet(_ context.Context, _ string) (string, bool, error) {
	return "", true, nil
}

// SecurityGroupExists всегда возвращает (true, nil).
func (NoopVPCClient) SecurityGroupExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}

// AddressExists всегда возвращает (true, nil).
func (NoopVPCClient) AddressExists(_ context.Context, _ string) (bool, error) { return true, nil }
