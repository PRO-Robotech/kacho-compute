package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	svc "github.com/PRO-Robotech/kacho-compute/internal/service"
)

// ImageHandler реализует computev1.ImageServiceServer.
type ImageHandler struct {
	computev1.UnimplementedImageServiceServer
	svc *svc.ImageService
}

// NewImageHandler создаёт ImageHandler.
func NewImageHandler(svc *svc.ImageService) *ImageHandler {
	return &ImageHandler{svc: svc}
}

func (h *ImageHandler) Get(ctx context.Context, req *computev1.GetImageRequest) (*computev1.Image, error) {
	if req.ImageId == "" {
		return nil, status.Error(codes.InvalidArgument, "image_id required")
	}
	img, err := h.svc.Get(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}
	return imageToProto(img), nil
}

func (h *ImageHandler) List(ctx context.Context, req *computev1.ListImagesRequest) (*computev1.ListImagesResponse, error) {
	images, nextToken, err := h.svc.List(ctx, req.Filter, svc.Pagination{
		PageToken: req.PageToken,
		PageSize:  req.PageSize,
	})
	if err != nil {
		return nil, err
	}

	resp := &computev1.ListImagesResponse{NextPageToken: nextToken}
	for _, img := range images {
		resp.Images = append(resp.Images, imageToProto(img))
	}
	return resp, nil
}

// ---- domain → proto ----

func imageToProto(img *domain.Image) *computev1.Image {
	return &computev1.Image{
		Id:          img.ID,
		Name:        img.Name,
		Description: img.Description,
		Family:      img.Family,
		OsType:      img.OsType,
		Size:        img.Size,
		Status:      computev1.ImageStatus(img.Status),
	}
}
