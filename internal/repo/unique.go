// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// isUniqueViolation — Postgres unique-constraint violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	s := err.Error()
	return strings.Contains(s, "23505") || strings.Contains(s, "duplicate key value")
}

// isFKViolation — Postgres foreign_key_violation (SQLSTATE 23503). Маппится в
// gRPC FailedPrecondition ("The <resource> is being used").
func isFKViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	s := err.Error()
	return strings.Contains(s, "23503") || strings.Contains(s, "violates foreign key")
}

// isCheckViolation — Postgres check_violation (SQLSTATE 23514). Легитимный
// bad-input по user-reachable CHECK-constraint → gRPC InvalidArgument
// (data-integrity.md SQLSTATE→gRPC table).
func isCheckViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23514"
	}
	return strings.Contains(err.Error(), "23514")
}

// isExclusionViolation — Postgres exclusion_violation (SQLSTATE 23P01). Состояние
// ресурса не позволяет (пересечение EXCLUDE-range) → gRPC FailedPrecondition
// (data-integrity.md SQLSTATE→gRPC table).
func isExclusionViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23P01"
	}
	return strings.Contains(err.Error(), "23P01")
}

// wrapPgErr классифицирует pgx-ошибку и возвращает sentinel-ошибку из
// service-пакета. НЕ leak'ает raw PG-сообщение клиенту: неизвестные классы → ErrInternal.
//
// kind/id — для NotFound сообщений (имя ресурса + id).
func wrapPgErr(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if id != "" {
			return fmt.Errorf("%w: %s %s not found", ports.ErrNotFound, kind, id)
		}
		return ports.ErrNotFound
	}
	if isUniqueViolation(err) {
		return ports.ErrAlreadyExists
	}
	if isFKViolation(err) {
		return fmt.Errorf("%w: The %s %s is being used", ports.ErrFailedPrecondition, strings.ToLower(kind), id)
	}
	if isCheckViolation(err) {
		// Фиксированный текст: не leak'аем raw CHECK-detail (constraint-имя /
		// pg-message) наружу — только класс ошибки.
		return ports.ErrInvalidArg
	}
	if isExclusionViolation(err) {
		return ports.ErrFailedPrecondition
	}
	return ports.ErrInternal
}
