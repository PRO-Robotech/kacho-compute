package check

import (
	"context"
	"strings"

	"google.golang.org/grpc"

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
// Когда IAM возвращает allowed=false с reason "no path" (нет FGA-tuple для
// объекта), Check возвращает authz.ErrNoPath — сигнал interceptor'у пропустить
// запрос к handler'у (который вернёт NOT_FOUND из DB) вместо 403.
func (c *IAMCheckClient) Check(ctx context.Context, subjectID, relation, object string) (bool, error) {
	resp, err := c.cli.Check(ctx, &iamv1.CheckRequest{
		SubjectId: subjectID,
		Relation:  relation,
		Object:    object,
	})
	if err != nil {
		return false, err
	}
	if !resp.GetAllowed() && strings.Contains(resp.GetReason(), "no path") {
		return false, authz.ErrNoPath
	}
	return resp.GetAllowed(), nil
}

var _ authz.CheckClient = (*IAMCheckClient)(nil)
