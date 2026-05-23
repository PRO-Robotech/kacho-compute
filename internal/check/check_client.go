package check

import (
	"context"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/authz"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// IAMCheckClient — gRPC adapter, реализующий port `authz.CheckClient`
// поверх `kacho-iam.InternalIAMService.Check`.
type IAMCheckClient struct {
	cli iamv1.InternalIAMServiceClient
}

// NewIAMCheckClient создаёт adapter. conn — `*grpc.ClientConn`/`ClientConnInterface`
// к internal-port'у kacho-iam (обычно `kacho-iam.kacho.svc.cluster.local:9091`).
func NewIAMCheckClient(conn grpc.ClientConnInterface) *IAMCheckClient {
	return &IAMCheckClient{cli: iamv1.NewInternalIAMServiceClient(conn)}
}

// Check вызывает `InternalIAMService.Check`.
//
// W1.4 (KAC-140): outgoing ctx обёрнут `auth.PropagateOutgoing`, чтобы iam-side
// `grpcsrv.UnaryPrincipalExtract` увидел реального caller'а, а не SystemPrincipal()
// = user:bootstrap. Mirror of kacho-vpc fix. См.
// `docs/specs/sub-phase-W1.4-principal-propagation-acceptance.md`.
func (c *IAMCheckClient) Check(ctx context.Context, subjectID, relation, object string) (bool, error) {
	resp, err := c.cli.Check(auth.PropagateOutgoing(ctx), &iamv1.CheckRequest{
		SubjectId: subjectID,
		Relation:  relation,
		Object:    object,
	})
	if err != nil {
		return false, err
	}
	return resp.GetAllowed(), nil
}

var _ authz.CheckClient = (*IAMCheckClient)(nil)
