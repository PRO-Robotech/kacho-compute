package clients

import (
	"context"

	"google.golang.org/grpc"

	vpcv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"
)

// SubnetClient реализует service.SubnetClient через gRPC к kacho-vpc.
type SubnetClient struct {
	client vpcv1.VpcInternalServiceClient
}

// NewSubnetClient создаёт SubnetClient.
func NewSubnetClient(conn *grpc.ClientConn) *SubnetClient {
	return &SubnetClient{
		client: vpcv1.NewVpcInternalServiceClient(conn),
	}
}

// Exists проверяет существование Subnet.
func (c *SubnetClient) Exists(ctx context.Context, subnetUID string) (bool, error) {
	resp, err := c.client.SubnetExists(ctx, &vpcv1.SubnetExistsRequest{Uid: subnetUID})
	if err != nil {
		return false, err
	}
	return resp.GetExists(), nil
}
