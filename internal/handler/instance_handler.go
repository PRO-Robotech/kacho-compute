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

// InstanceHandler реализует pb.InstanceServiceServer.
type InstanceHandler struct {
	pb.UnimplementedInstanceServiceServer
	svc *service.InstanceService
	hub *watch.Hub
}

// NewInstanceHandler создаёт InstanceHandler.
func NewInstanceHandler(svc *service.InstanceService, hub *watch.Hub) *InstanceHandler {
	return &InstanceHandler{svc: svc, hub: hub}
}

func (h *InstanceHandler) Upsert(ctx context.Context, req *pb.InstanceUpsertRequest) (*pb.InstanceUpsertResponse, error) {
	resp := &pb.InstanceUpsertResponse{}
	for _, in := range req.GetInstances() {
		// Запрет #6: status в upsert не допускается
		if in.GetStatus() != nil {
			return nil, coreerrors.InvalidArgument().AddFieldViolation("status", "status cannot be set via Upsert, use Internal.UpdateInstanceStatus").Err()
		}
		inst := repo.ProtoInstanceToDomain(in)
		result, err := h.svc.Upsert(ctx, inst)
		if err != nil {
			return nil, err
		}
		resp.Instances = append(resp.Instances, repo.DomainInstanceToProto(result))
	}
	return resp, nil
}

func (h *InstanceHandler) Delete(ctx context.Context, req *pb.InstanceDeleteRequest) (*pb.InstanceDeleteResponse, error) {
	for _, item := range req.GetItems() {
		uid := item.GetUid()
		if uid == "" {
			return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
		}
		if err := h.svc.Delete(ctx, uid); err != nil {
			return nil, err
		}
	}
	return &pb.InstanceDeleteResponse{}, nil
}

func (h *InstanceHandler) List(ctx context.Context, req *pb.InstanceListRequest) (*pb.InstanceListResponse, error) {
	selectors := protoSelectorsToService(req.GetSelectors())
	page := service.Pagination{
		PageToken: req.GetPageToken(),
		PageSize:  req.GetPageSize(),
	}

	instances, nextToken, snapshotRV, err := h.svc.List(ctx, selectors, page)
	if err != nil {
		return nil, err
	}

	resp := &pb.InstanceListResponse{
		ResourceVersion: int64ToString(snapshotRV),
		NextPageToken:   nextToken,
	}
	for _, inst := range instances {
		resp.Instances = append(resp.Instances, repo.DomainInstanceToProto(inst))
	}
	return resp, nil
}

func (h *InstanceHandler) Watch(req *pb.InstanceWatchRequest, stream pb.InstanceService_WatchServer) error {
	ctx := stream.Context()

	var fromRV int64
	if rvStr := req.GetResourceVersion(); rvStr != "" {
		var err error
		fromRV, err = strconv.ParseInt(rvStr, 10, 64)
		if err != nil {
			return status.Errorf(codes.InvalidArgument, "invalid resource_version: %v", err)
		}
	}

	sub, err := h.hub.Subscribe(ctx, fromRV, buildInstanceMatcher(req.GetSelectors()))
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

func (h *InstanceHandler) Restart(ctx context.Context, req *pb.InstanceRestartRequest) (*pb.InstanceRestartResponse, error) {
	uid := req.GetUid()
	if uid == "" {
		return nil, coreerrors.InvalidArgument().AddFieldViolation("uid", "uid is required").Err()
	}
	result, err := h.svc.Restart(ctx, uid)
	if err != nil {
		return nil, err
	}
	return &pb.InstanceRestartResponse{Instance: repo.DomainInstanceToProto(result)}, nil
}

func buildInstanceMatcher(_ []*commonv1.Selector) watch.SelectorMatcher {
	return func(evt watch.Event) bool {
		return evt.ResourceKind == "Instance"
	}
}
