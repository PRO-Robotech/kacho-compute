package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-compute/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// ImageHandler реализует computev1.ImageServiceServer (тонкий transport-слой).
// access-bindings RPC наследуются из UnimplementedImageServiceServer (Unimplemented).
type ImageHandler struct {
	computev1.UnimplementedImageServiceServer
	svc        *svc.ImageService
	listFilter authzfilter.Filter
}

// NewImageHandler создаёт ImageHandler. listFilter может быть nil — тогда
// FGA-фильтрация на List отключена (dev/breakglass).
func NewImageHandler(s *svc.ImageService, listFilter authzfilter.Filter) *ImageHandler {
	return &ImageHandler{svc: s, listFilter: listFilter}
}

// Get возвращает Image по id.
func (h *ImageHandler) Get(ctx context.Context, req *computev1.GetImageRequest) (*computev1.Image, error) {
	if req.ImageId == "" {
		return nil, status.Error(codes.InvalidArgument, "image_id required")
	}
	i, err := h.svc.Get(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, i.ProjectID); err != nil {
		return nil, err
	}
	return protoconv.Image(i), nil
}

// GetLatestByFamily возвращает самый новый Image в family.
func (h *ImageHandler) GetLatestByFamily(ctx context.Context, req *computev1.GetImageLatestByFamilyRequest) (*computev1.Image, error) {
	if err := AssertFolderOwnership(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	i, err := h.svc.GetLatestByFamily(ctx, req.ProjectId, req.Family)
	if err != nil {
		return nil, err
	}
	return protoconv.Image(i), nil
}

// List возвращает список образов в folder.
//
// KAC-127 Phase 4: вызов фильтруется через iam.AuthorizeService.ListObjects
// (caller subject → allowed image_ids).
func (h *ImageHandler) List(ctx context.Context, req *computev1.ListImagesRequest) (*computev1.ListImagesResponse, error) {
	if err := AssertFolderOwnership(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	dec, err := resolveListFilter(ctx, h.listFilter, authzfilter.ResourceTypeImage, authzfilter.ActionImageRead)
	if err != nil {
		return nil, err
	}
	filter := svc.ImageFilter{ProjectID: req.ProjectId, Filter: req.Filter}
	if !dec.bypass {
		if len(dec.allowedIDs) == 0 {
			return &computev1.ListImagesResponse{}, nil
		}
		filter.AllowedIDs = dec.allowedIDs
	}
	imgs, nextToken, err := h.svc.List(ctx, filter,
		svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListImagesResponse{NextPageToken: nextToken}
	for _, i := range imgs {
		resp.Images = append(resp.Images, protoconv.Image(i))
	}
	return resp, nil
}

// Create инициирует создание Image.
func (h *ImageHandler) Create(ctx context.Context, req *computev1.CreateImageRequest) (*operationpb.Operation, error) {
	if err := AssertFolderOwnership(ctx, req.ProjectId); err != nil {
		return nil, err
	}
	op, err := h.svc.Create(ctx, svc.CreateImageReq{
		ProjectID:          req.ProjectId,
		Name:               req.Name,
		Description:        req.Description,
		Labels:             req.Labels,
		Family:             req.Family,
		MinDiskSize:        req.MinDiskSize,
		ProductIDs:         req.ProductIds,
		ImageID:            req.GetImageId(),
		DiskID:             req.GetDiskId(),
		SnapshotID:         req.GetSnapshotId(),
		URI:                req.GetUri(),
		Os:                 req.Os,
		Pooled:             req.Pooled,
		HardwareGeneration: req.HardwareGeneration,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Update инициирует обновление Image.
func (h *ImageHandler) Update(ctx context.Context, req *computev1.UpdateImageRequest) (*operationpb.Operation, error) {
	if req.ImageId == "" {
		return nil, status.Error(codes.InvalidArgument, "image_id required")
	}
	i, err := h.svc.Get(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, i.ProjectID); err != nil {
		return nil, err
	}
	var mask []string
	if req.UpdateMask != nil {
		mask = req.UpdateMask.Paths
	}
	op, err := h.svc.Update(ctx, svc.UpdateImageReq{
		ImageID:     req.ImageId,
		Name:        req.Name,
		Description: req.Description,
		Labels:      req.Labels,
		MinDiskSize: req.MinDiskSize,
		UpdateMask:  mask,
	})
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Delete инициирует удаление Image.
func (h *ImageHandler) Delete(ctx context.Context, req *computev1.DeleteImageRequest) (*operationpb.Operation, error) {
	if req.ImageId == "" {
		return nil, status.Error(codes.InvalidArgument, "image_id required")
	}
	i, err := h.svc.Get(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, i.ProjectID); err != nil {
		return nil, err
	}
	op, err := h.svc.Delete(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// ListOperations возвращает операции для Image.
func (h *ImageHandler) ListOperations(ctx context.Context, req *computev1.ListImageOperationsRequest) (*computev1.ListImageOperationsResponse, error) {
	if req.ImageId == "" {
		return nil, status.Error(codes.InvalidArgument, "image_id required")
	}
	i, err := h.svc.Get(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}
	if err := AssertFolderOwnership(ctx, i.ProjectID); err != nil {
		return nil, err
	}
	ops, nextToken, err := h.svc.ListOperations(ctx, req.ImageId, svc.Pagination{PageToken: req.PageToken, PageSize: req.PageSize})
	if err != nil {
		return nil, err
	}
	resp := &computev1.ListImageOperationsResponse{NextPageToken: nextToken}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}
