package handler

import (
	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// operationToProto конвертирует domain Operation в proto Operation.
func operationToProto(op *operations.Operation) *operationv1.Operation {
	p := &operationv1.Operation{
		Id:          op.ID,
		Description: op.Description,
		CreatedAt:   timestamppb.New(op.CreatedAt),
		ModifiedAt:  timestamppb.New(op.ModifiedAt),
		Done:        op.Done,
		Metadata:    op.Metadata,
	}
	if op.Error != nil {
		p.Result = &operationv1.Operation_Error{Error: op.Error}
	} else if op.Response != nil {
		p.Result = &operationv1.Operation_Response{Response: op.Response}
	}
	return p
}
