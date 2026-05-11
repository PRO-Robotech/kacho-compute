package service

import (
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mapRepoErr — единая трансляция repo-sentinel в gRPC status (копия VPC).
//
// Sentinel-prefix (`failed precondition: `, `not found`, ...) удаляется при
// преобразовании в gRPC-сообщение, чтобы клиент видел verbatim YC text без
// internal-обёртки.
//
// Fallthrough: неклассифицированный err → codes.Internal с фиксированным
// "internal database error" (закрывает info-leak vector через Operation.error.message).
func mapRepoErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, stripSentinel(err, ErrNotFound))
	case errors.Is(err, ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, stripSentinel(err, ErrAlreadyExists))
	case errors.Is(err, ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, stripSentinel(err, ErrFailedPrecondition))
	case errors.Is(err, ErrInvalidArg):
		return status.Error(codes.InvalidArgument, stripSentinel(err, ErrInvalidArg))
	case errors.Is(err, ErrInternal):
		return status.Error(codes.Internal, "internal database error")
	}
	// Если err уже gRPC-status (например из самого service-слоя) — пробрасываем.
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		return err
	}
	// Defensive: raw error из repo без обёртки → generic Internal без leak'а текста.
	return status.Error(codes.Internal, "internal database error")
}

// stripSentinel — извлекает «полезную» часть сообщения (после «sentinel: »),
// чтобы выдать клиенту verbatim text без internal-обёртки sentinel-ошибки.
func stripSentinel(err, sentinel error) string {
	msg := err.Error()
	prefix := sentinel.Error() + ": "
	if rest, ok := strings.CutPrefix(msg, prefix); ok {
		return rest
	}
	return msg
}
