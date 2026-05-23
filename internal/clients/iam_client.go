// Package clients содержит gRPC-адаптеры к peer-сервисам (Clean Architecture
// outbound adapters): kacho-iam (ProjectService) и kacho-vpc
// (Subnet/SecurityGroup/Address). Реализуют port-интерфейсы из internal/ports.
//
// KAC-106 (E1): peer для project-existence-check переключён с
// kacho-resource-manager.FolderService.Get на kacho-iam.ProjectService.Get.
// File-name retained for git-history continuity.
package clients

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// withPrincipalMD propagates the caller's principal onto the outgoing gRPC
// metadata (KAC-133, mirrors kacho-vpc/internal/clients/iam_client.go KAC-127 Bug-2).
//
// kacho-iam's public ProjectService.Get carries a tenant scope-filter: it
// returns NOT_FOUND unless the caller is the owning Account's owner. The
// compute Operation worker validates the project via ProjectService.Get; without
// forwarding the principal the peer sees an anonymous/system call, returns
// NOT_FOUND, and Create fails its project-exists check — causing the Operation
// to error asynchronously so the resource is never persisted.
func withPrincipalMD(ctx context.Context) context.Context {
	p := operations.PrincipalFromContext(ctx)
	if p.ID == "" || p.Type == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx,
		grpcsrv.MDKeyPrincipalType, p.Type,
		grpcsrv.MDKeyPrincipalID, p.ID,
		grpcsrv.MDKeyPrincipalDisplay, p.DisplayName,
	)
}

// projectExistsTTL — TTL положительного результата Exists.
const projectExistsTTL = 30 * time.Second

// ProjectClient реализует service.ProjectClient через gRPC к kacho-iam
// с TTL-кешем для Exists (hot path: каждый Create/Move).
type ProjectClient struct {
	cli iamv1.ProjectServiceClient

	mu     sync.RWMutex
	exists map[string]time.Time
}

// NewProjectClient создаёт ProjectClient.
func NewProjectClient(conn *grpc.ClientConn) *ProjectClient {
	return &ProjectClient{
		cli:    iamv1.NewProjectServiceClient(conn),
		exists: make(map[string]time.Time),
	}
}

// Exists проверяет существование Project через kacho-iam.ProjectService.Get.
// Положительный результат кешируется на projectExistsTTL (убирает gRPC RTT
// из hot-path при burst-нагрузке). NotFound НЕ кешируется (свеже-созданный
// project быстро становится виден).
func (c *ProjectClient) Exists(ctx context.Context, projectID string) (bool, error) {
	c.mu.RLock()
	exp, ok := c.exists[projectID]
	c.mu.RUnlock()
	if ok && time.Now().Before(exp) {
		return true, nil
	}

	var exists bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.cli.Get(withPrincipalMD(ctx), &iamv1.GetProjectRequest{ProjectId: projectID})
		if rerr != nil {
			st, ok := status.FromError(rerr)
			if ok && (st.Code() == codes.NotFound || st.Code() == codes.InvalidArgument) {
				// NotFound  → project does not exist.
				// InvalidArgument → project id is malformed (wrong prefix / length);
				//   IAM validates the id format and returns InvalidArgument for
				//   garbage ids. Treat as "not found" so callers receive the
				//   canonical "Folder with id X not found" async error.
				exists = false
				return nil
			}
			return rerr
		}
		exists = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if exists {
		c.mu.Lock()
		c.exists[projectID] = time.Now().Add(projectExistsTTL)
		c.mu.Unlock()
	}
	return exists, nil
}

// NoopProjectClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true
// (Exists всегда true) и для unit/newman без поднятого kacho-iam.
type NoopProjectClient struct{}

// Exists всегда возвращает (true, nil).
func (NoopProjectClient) Exists(_ context.Context, _ string) (bool, error) { return true, nil }
