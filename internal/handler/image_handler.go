package handler

import (
	"context"
	"strconv"

	pb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	commonv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// ImageHandler реализует pb.ImageServiceServer (read-only).
type ImageHandler struct {
	pb.UnimplementedImageServiceServer
	svc *service.ImageService
}

// NewImageHandler создаёт ImageHandler.
func NewImageHandler(svc *service.ImageService) *ImageHandler {
	return &ImageHandler{svc: svc}
}

func (h *ImageHandler) Get(ctx context.Context, req *pb.ImageGetRequest) (*pb.ImageGetResponse, error) {
	img, err := h.svc.GetByUID(ctx, req.GetUid())
	if err != nil {
		return nil, err
	}
	return &pb.ImageGetResponse{Image: domainImageToProto(img)}, nil
}

func (h *ImageHandler) List(ctx context.Context, req *pb.ImageListRequest) (*pb.ImageListResponse, error) {
	selectors := protoSelectorsToService(req.GetSelectors())
	page := service.Pagination{
		PageToken: req.GetPageToken(),
		PageSize:  req.GetPageSize(),
	}

	images, nextToken, snapshotRV, err := h.svc.List(ctx, selectors, page)
	if err != nil {
		return nil, err
	}

	resp := &pb.ImageListResponse{
		ResourceVersion: int64ToString(snapshotRV),
		NextPageToken:   nextToken,
	}
	for _, img := range images {
		resp.Images = append(resp.Images, domainImageToProto(img))
	}
	return resp, nil
}

func domainImageToProto(img *domain.Image) *pb.Image {
	meta := &commonv1.ResourceMeta{
		Uid:             img.UID,
		Name:            img.Name,
		Labels:          img.Labels,
		ResourceVersion: strconv.FormatInt(img.ResourceVersion, 10),
		Generation:      img.Generation,
	}
	if !img.CreationTimestamp.IsZero() {
		meta.CreationTimestamp = timestamppb.New(img.CreationTimestamp)
	}

	return &pb.Image{
		Metadata: meta,
		Spec: &pb.ImageSpec{
			DisplayName: img.DisplayName,
			Description: img.Description,
			Family:      img.Family,
		},
		Status: &pb.ImageStatus{
			State: pb.ImageStatus_STATE_READY,
		},
	}
}

