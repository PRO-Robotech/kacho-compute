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

// SnapshotHandler реализует pb.SnapshotServiceServer.
type SnapshotHandler struct {
	pb.UnimplementedSnapshotServiceServer
	svc *service.SnapshotService
	hub *watch.Hub
}

// NewSnapshotHandler создаёт SnapshotHandler.
func NewSnapshotHandler(svc *service.SnapshotService, hub *watch.Hub) *SnapshotHandler {
	return &SnapshotHandler{svc: svc, hub: hub}
}

func (h *SnapshotHandler) Upsert(ctx context.Context, req *pb.SnapshotUpsertRequest) (*pb.SnapshotUpsertResponse, error) {
	resp := &pb.SnapshotUpsertResponse{}
	for _, in := range req.GetSnapshots() {
		if in.GetStatus() != nil {
			return nil, coreerrors.InvalidArgument().AddFieldViolation("status", "status cannot be set via Upsert, use Internal.UpdateSnapshotStatus").Err()
		}
		snap := repo.ProtoSnapshotToDomain(in)
		result, err := h.svc.Upsert(ctx, snap)
		if err != nil {
			return nil, err
		}
		resp.Snapshots = append(resp.Snapshots, repo.DomainSnapshotToProto(result))
	}
	return resp, nil
}

func (h *SnapshotHandler) Delete(ctx context.Context, req *pb.SnapshotDeleteRequest) (*pb.SnapshotDeleteResponse, error) {
	for _, item := range req.GetItems() {
		uid := item.GetUid()
		if uid == "" {
			return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
		}
		if err := h.svc.Delete(ctx, uid); err != nil {
			return nil, err
		}
	}
	return &pb.SnapshotDeleteResponse{}, nil
}

func (h *SnapshotHandler) List(ctx context.Context, req *pb.SnapshotListRequest) (*pb.SnapshotListResponse, error) {
	selectors := protoSelectorsToService(req.GetSelectors())
	page := service.Pagination{
		PageToken: req.GetPageToken(),
		PageSize:  req.GetPageSize(),
	}

	snaps, nextToken, snapshotRV, err := h.svc.List(ctx, selectors, page)
	if err != nil {
		return nil, err
	}

	resp := &pb.SnapshotListResponse{
		ResourceVersion: int64ToString(snapshotRV),
		NextPageToken:   nextToken,
	}
	for _, snap := range snaps {
		resp.Snapshots = append(resp.Snapshots, repo.DomainSnapshotToProto(snap))
	}
	return resp, nil
}

func (h *SnapshotHandler) Watch(req *pb.SnapshotWatchRequest, stream pb.SnapshotService_WatchServer) error {
	ctx := stream.Context()

	var fromRV int64
	if rvStr := req.GetResourceVersion(); rvStr != "" {
		var err error
		fromRV, err = strconv.ParseInt(rvStr, 10, 64)
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "invalid resource_version: %v", err)
		}
	}

	sub, err := h.hub.Subscribe(ctx, fromRV, buildSnapshotMatcher(req.GetSelectors()))
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

func buildSnapshotMatcher(_ []*commonv1.Selector) watch.SelectorMatcher {
	return func(evt watch.Event) bool {
		return evt.ResourceKind == "Snapshot"
	}
}
