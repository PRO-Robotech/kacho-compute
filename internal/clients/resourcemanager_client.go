// Package clients содержит gRPC-адаптеры к peer-сервисам (Clean Architecture
// outbound adapters): kacho-resource-manager (FolderService) и kacho-vpc
// (Subnet/SecurityGroup/Address). Реализуют port-интерфейсы из internal/ports.
package clients

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	rmv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/resourcemanager/v1"
)

// folderExistsTTL — как долго кешируется положительный результат Exists.
// Folder existence стабильна, но не вечна. NotFound НЕ кешируется (folder
// может быть создан в любой момент). Зеркалит kacho-vpc/internal/clients.
const folderExistsTTL = 30 * time.Second

// FolderClient реализует service.FolderClient через gRPC к kacho-resource-manager
// с TTL-кешем для Exists (hot path: каждый Create/Move).
type FolderClient struct {
	cli rmv1.FolderServiceClient

	mu     sync.RWMutex
	exists map[string]time.Time
}

// NewFolderClient создаёт FolderClient.
func NewFolderClient(conn *grpc.ClientConn) *FolderClient {
	return &FolderClient{
		cli:    rmv1.NewFolderServiceClient(conn),
		exists: make(map[string]time.Time),
	}
}

// Exists проверяет существование Folder. Положительный результат кешируется на
// folderExistsTTL (убирает gRPC RTT из hot-path при burst-нагрузке).
func (c *FolderClient) Exists(ctx context.Context, folderID string) (bool, error) {
	c.mu.RLock()
	exp, ok := c.exists[folderID]
	c.mu.RUnlock()
	if ok && time.Now().Before(exp) {
		return true, nil
	}

	var exists bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.cli.Get(ctx, &rmv1.GetFolderRequest{FolderId: folderID})
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
		c.exists[folderID] = time.Now().Add(folderExistsTTL)
		c.mu.Unlock()
	}
	return exists, nil
}

// NoopFolderClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true
// (Exists всегда true) и для unit/newman без поднятого resource-manager.
type NoopFolderClient struct{}

// Exists всегда возвращает (true, nil).
func (NoopFolderClient) Exists(_ context.Context, _ string) (bool, error) { return true, nil }
