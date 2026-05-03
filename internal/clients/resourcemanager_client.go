package clients

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	rmv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/resourcemanager/v1"
)

// FolderClient реализует service.FolderClient через gRPC к resource-manager.
// Retry-policy: codes.Unavailable, exponential backoff 100ms..5s, max 30s.
type FolderClient struct {
	cli rmv1.FolderServiceClient
}

// NewFolderClient создаёт FolderClient.
func NewFolderClient(conn *grpc.ClientConn) *FolderClient {
	return &FolderClient{cli: rmv1.NewFolderServiceClient(conn)}
}

// Exists проверяет существование Folder. Возвращает true если found.
func (c *FolderClient) Exists(ctx context.Context, folderID string) (bool, error) {
	var exists bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.cli.Get(ctx, &rmv1.GetFolderRequest{FolderId: folderID})
		if rerr != nil {
			st, ok := status.FromError(rerr)
			if ok && st.Code() == codes.NotFound {
				exists = false
				return nil // не retry — folder просто не существует
			}
			return rerr
		}
		exists = true
		return nil
	})
	return exists, err
}
