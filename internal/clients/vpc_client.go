package clients

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	vpcv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"
)

// SubnetClient реализует service.SubnetClient через gRPC к kacho-vpc.
// Retry-policy: codes.Unavailable, exponential backoff 100ms..5s, max 30s.
type SubnetClient struct {
	cli vpcv1.SubnetServiceClient
}

// NewSubnetClient создаёт SubnetClient.
func NewSubnetClient(conn *grpc.ClientConn) *SubnetClient {
	return &SubnetClient{cli: vpcv1.NewSubnetServiceClient(conn)}
}

// Exists проверяет существование Subnet. Возвращает true если found.
func (c *SubnetClient) Exists(ctx context.Context, subnetID string) (bool, error) {
	var exists bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.cli.Get(ctx, &vpcv1.GetSubnetRequest{SubnetId: subnetID})
		if rerr != nil {
			st, ok := status.FromError(rerr)
			if ok && st.Code() == codes.NotFound {
				exists = false
				return nil
			}
			return rerr
		}
		exists = true
		return nil
	})
	return exists, err
}
