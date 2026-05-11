package handler

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// internalMapErr — admin/Internal-handler error mapper. Гарантирует что raw
// pgx-text (хранит hostname/db/query) не уходит в response даже на
// cluster-internal listener (:9091). Зеркалит kacho-vpc/internal/handler/internal_maperr.go.
func internalMapErr(tag string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, service.ErrNotFound):
		return status.Error(codes.NotFound, service.ErrNotFound.Error())
	case errors.Is(err, service.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, service.ErrAlreadyExists.Error())
	case errors.Is(err, service.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, service.ErrFailedPrecondition.Error())
	case errors.Is(err, service.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, service.ErrInvalidArg.Error())
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		return err
	}
	if tag == "" {
		tag = "internal error"
	}
	return status.Error(codes.Internal, tag)
}
