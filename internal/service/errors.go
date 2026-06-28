// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import "github.com/PRO-Robotech/kacho-compute/internal/ports"

// Sentinel-ошибки живут в leaf-пакете `internal/ports` — это позволяет общему
// test-helper'у `internal/ports/portmock` возвращать их без зависимости от
// `internal/service`. Здесь — ре-экспорт через `var`-alias'ы (те же
// error-value, поэтому `errors.Is(err, service.ErrNotFound)` работает).
// Зеркалит kacho-vpc/internal/service/errors.go.
var (
	// ErrNotFound возвращается, когда ресурс не найден.
	ErrNotFound = ports.ErrNotFound
	// ErrAlreadyExists возвращается при нарушении UNIQUE constraint.
	ErrAlreadyExists = ports.ErrAlreadyExists
	// ErrInvalidArg возвращается при некорректных входных данных.
	ErrInvalidArg = ports.ErrInvalidArg
	// ErrFailedPrecondition возвращается, когда операция отклонена из-за
	// состояния ресурса. Маппится в gRPC FailedPrecondition.
	ErrFailedPrecondition = ports.ErrFailedPrecondition
	// ErrInternal — generic-ошибка для неклассифицированных DB-проблем.
	ErrInternal = ports.ErrInternal
)
