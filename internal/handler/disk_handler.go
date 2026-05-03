package handler

import (
	"context"
	"errors"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
	commonv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/common/v1"
	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
	"github.com/PRO-Robotech/kacho-corelib/watch"

	"github.com/PRO-Robotech/kacho-compute/internal/repo"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// DiskHandler реализует pb.DiskServiceServer.
type DiskHandler struct {
	pb.UnimplementedDiskServiceServer
	svc *service.DiskService
	hub *watch.Hub
}

// NewDiskHandler создаёт DiskHandler.
func NewDiskHandler(svc *service.DiskService, hub *watch.Hub) *DiskHandler {
	return &DiskHandler{svc: svc, hub: hub}
}

func (h *DiskHandler) Upsert(ctx context.Context, req *pb.DiskUpsertRequest) (*pb.DiskUpsertResponse, error) {
	resp := &pb.DiskUpsertResponse{}
	for _, in := range req.GetDisks() {
		// Запрет #6: status в upsert не допускается
		if in.GetStatus() != nil {
			return nil, coreerrors.InvalidArgument().AddFieldViolation("status", "status cannot be set via Upsert, use Internal.UpdateDiskStatus").Err()
		}
		disk := repo.ProtoDiskToDomain(in)
		result, err := h.svc.Upsert(ctx, disk)
		if err != nil {
			return nil, err
		}
		resp.Disks = append(resp.Disks, repo.DomainDiskToProto(result))
	}
	return resp, nil
}

func (h *DiskHandler) Delete(ctx context.Context, req *pb.DiskDeleteRequest) (*pb.DiskDeleteResponse, error) {
	for _, item := range req.GetItems() {
		uid := item.GetUid()
		if uid == "" {
			return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
		}
		if err := h.svc.Delete(ctx, uid); err != nil {
			return nil, err
		}
	}
	return &pb.DiskDeleteResponse{}, nil
}

func (h *DiskHandler) List(ctx context.Context, req *pb.DiskListRequest) (*pb.DiskListResponse, error) {
	selectors := protoSelectorsToService(req.GetSelectors())
	page := service.Pagination{
		PageToken: req.GetPageToken(),
		PageSize:  req.GetPageSize(),
	}

	disks, nextToken, snapshotRV, err := h.svc.List(ctx, selectors, page)
	if err != nil {
		return nil, err
	}

	resp := &pb.DiskListResponse{
		ResourceVersion: int64ToString(snapshotRV),
		NextPageToken:   nextToken,
	}
	for _, disk := range disks {
		resp.Disks = append(resp.Disks, repo.DomainDiskToProto(disk))
	}
	return resp, nil
}

func (h *DiskHandler) Watch(req *pb.DiskWatchRequest, stream pb.DiskService_WatchServer) error {
	ctx := stream.Context()

	var fromRV int64
	if rvStr := req.GetResourceVersion(); rvStr != "" {
		var err error
		fromRV, err = strconv.ParseInt(rvStr, 10, 64)
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "invalid resource_version: %v", err)
		}
	}

	sub, err := h.hub.Subscribe(ctx, fromRV, buildDiskMatcher(req.GetSelectors()))
	if err != nil {
		if errors.Is(err, watch.ErrGone) {
			return status.Error(codes.OutOfRange, "resourceVersion too old, please relist")
		}
		return err
	}
	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt, ok := <-sub.C:
			if !ok {
				return nil
			}
			if sendErr := stream.Send(watchEventToProto(evt)); sendErr != nil {
				return sendErr
			}
		}
	}
}

func buildDiskMatcher(_ []*commonv1.Selector) watch.SelectorMatcher {
	return func(evt watch.Event) bool {
		return evt.ResourceKind == "Disk"
	}
}
