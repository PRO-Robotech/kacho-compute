package authzfilter

import (
	"context"

	"google.golang.org/grpc"

	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// NewIAMAuthorizeClient оборачивает gRPC conn в AuthorizeClient.
// conn обычно указывает на kacho-iam internal-port (:9091) — там живёт
// AuthorizeService.
func NewIAMAuthorizeClient(conn grpc.ClientConnInterface) AuthorizeClient {
	return &grpcAuthorizeClient{cli: iamv1.NewAuthorizeServiceClient(conn)}
}

type grpcAuthorizeClient struct {
	cli iamv1.AuthorizeServiceClient
}

func (g *grpcAuthorizeClient) ListObjects(ctx context.Context, req *iamv1.ListObjectsRequest, opts ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	return g.cli.ListObjects(ctx, req, opts...)
}
