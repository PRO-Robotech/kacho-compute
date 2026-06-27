// Package clients содержит gRPC-адаптеры к peer-сервисам (Clean Architecture
// outbound adapters): kacho-iam (ProjectService) и kacho-vpc
// (Subnet/SecurityGroup/Address). Реализуют port-интерфейсы из internal/ports.
//
// KAC-106 (E1): peer для project-existence-check переключён с
// kacho-resource-manager.FolderService.Get на kacho-iam.ProjectService.Get.
// File-name retained for git-history continuity.
//
// W1.4 (KAC-140): outgoing ctx обёрнут `auth.PropagateOutgoing` — peer-call
// несёт `x-kacho-principal-*` MD, чтобы iam-side scope-filter увидел реального
// caller'а (раньше — anonymous/system, NOT_FOUND, тихий fail Operation; mirror
// of kacho-vpc KAC-127 Bug-2 + W1.4 lift to corelib).
package clients

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iamv1 "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"
)

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
		_, rerr := c.cli.Get(auth.PropagateOutgoing(ctx), &iamv1.GetProjectRequest{ProjectId: projectID})
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
