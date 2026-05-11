package service

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// invalidArg формирует gRPC InvalidArgument с FieldViolation-деталью.
// Зеркалит kacho-vpc/internal/service/validate.go::invalidArg.
func invalidArg(field, desc string) error {
	st := status.New(codes.InvalidArgument, desc)
	br := &errdetails.BadRequest{
		FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: field, Description: desc},
		},
	}
	if withDetails, derr := st.WithDetails(br); derr == nil {
		return withDetails.Err()
	}
	return st.Err()
}

// requiredString возвращает InvalidArgument "<field> required" если v пуст.
func requiredString(field, v string) error {
	if v == "" {
		return status.Errorf(codes.InvalidArgument, "%s required", field)
	}
	return nil
}
