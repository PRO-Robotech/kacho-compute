package clients

import (
	"context"

	"google.golang.org/grpc"

	rmv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/resourcemanager/v1"
)

// FolderClient реализует service.FolderClient через gRPC к resource-manager.
type FolderClient struct {
	client rmv1.FolderInternalServiceClient
}

// NewFolderClient создаёт FolderClient.
func NewFolderClient(conn *grpc.ClientConn) *FolderClient {
	return &FolderClient{
		client: rmv1.NewFolderInternalServiceClient(conn),
	}
}

// Exists проверяет существование Folder.
func (c *FolderClient) Exists(ctx context.Context, folderUID string) (bool, error) {
	resp, err := c.client.Exists(ctx, &rmv1.ExistsRequest{Uid: folderUID})
	if err != nil {
		// При недоступности resource-manager — возвращаем false с ошибкой
		return false, err
	}
	return resp.GetExists(), nil
}
