// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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
// преобразовании в gRPC-сообщение, чтобы клиент видел чистый текст без
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

// mapRefErr транслирует ошибку existence-check ссылочного ресурса ИЗ ТОЙ ЖЕ БД
// (Image/Snapshot/Disk/DiskType lookup на request-path). Раньше эти call-site'ы
// слепо маппили ЛЮБУЮ non-nil ошибку в codes.NotFound "<Resource> <id> not found",
// из-за чего транзиентный сбой БД (обрыв соединения, deadline, query-error) во
// время lookup маскировался под перманентный NotFound — клиент не ретраил и вводился
// в заблуждение о несуществовании реально существующего ресурса (CWE-388).
//
// Теперь: настоящий not-found (repo вернул ErrNotFound) → codes.NotFound с
// детерминированным текстом "<Resource> <id> not found"; всё остальное
// (ErrInternal / raw pgx / транспорт) → делегируется mapRepoErr → codes.Internal
// (без leak'а текста) вместо ложного NotFound. Зеркалит дисциплину primary-Get.
func mapRefErr(err error, resource, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return status.Errorf(codes.NotFound, "%s %s not found", resource, id)
	}
	return mapRepoErr(err)
}

// mapZoneRefErr транслирует ошибку existence-check zone_id (через ZoneRegistry —
// kacho-geo geo.v1.ZoneService.Get; Geography принадлежит kacho-geo) в
// gRPC-status, сохраняя контракт compute: неизвестная зона → InvalidArgument
// "Zone <id> not found". Транспортная ошибка к kacho-geo (Unavailable)
// пробрасывается как Unavailable "zone check: <err>" (зеркалит
// folder/subnet-check).
func mapZoneRefErr(err error, zoneID string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return status.Errorf(codes.InvalidArgument, "Zone %s not found", zoneID)
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
		return status.Errorf(codes.InvalidArgument, "Zone %s not found", zoneID)
	}
	return status.Errorf(codes.Unavailable, "zone check: %v", err)
}

// stripSentinel — извлекает «полезную» часть сообщения (после «sentinel: »),
// чтобы выдать клиенту чистый текст без internal-обёртки sentinel-ошибки.
func stripSentinel(err, sentinel error) string {
	msg := err.Error()
	prefix := sentinel.Error() + ": "
	if rest, ok := strings.CutPrefix(msg, prefix); ok {
		return rest
	}
	return msg
}
