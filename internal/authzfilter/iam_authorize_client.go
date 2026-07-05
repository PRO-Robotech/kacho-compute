// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

import (
	"context"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/kacho-corelib/auth"
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

// ListObjects пробрасывает request в kacho-iam AuthorizeService.
//
// outgoing ctx обёрнут `auth.PropagateOutgoing`, чтобы iam-side
// `grpcsrv.UnaryPrincipalExtract` увидел реального caller'а, а не
// SystemPrincipal() = user:bootstrap. Без wrap'а IAM authzguard'ы видели
// "system:bootstrap" и отбивали ListObjects как
// "authz_anonymous_mutation_denied" → compute list-filter возвращал 403
// для всех user'ов независимо от их FGA-tuple'ов.
func (g *grpcAuthorizeClient) ListObjects(ctx context.Context, req *iamv1.ListObjectsRequest, opts ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	return g.cli.ListObjects(auth.PropagateOutgoing(ctx), req, opts...)
}
