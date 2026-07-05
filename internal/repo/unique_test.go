// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/kacho-compute/internal/ports"
)

// TestWrapPgErr_CheckAndExclusion фиксирует SQLSTATE→sentinel маппинг из
// data-integrity.md: 23514 (CHECK) → InvalidArgument, 23P01 (EXCLUDE) →
// FailedPrecondition. Раньше оба класса падали в default-ветку ErrInternal →
// codes.Internal "internal database error", из-за чего легитимный bad-input
// (нарушение user-reachable CHECK/EXCLUDE) выглядел как транзиентный серверный
// сбой и клиент ретраил перманентно неуспешный запрос (CWE-703).
func TestWrapPgErr_CheckAndExclusion(t *testing.T) {
	tests := []struct {
		name string
		code string
		want error
	}{
		{"check_violation 23514 → InvalidArg", "23514", ports.ErrInvalidArg},
		{"exclusion_violation 23P01 → FailedPrecondition", "23P01", ports.ErrFailedPrecondition},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pgErr := &pgconn.PgError{Code: tc.code, Message: "raw-pg-detail-must-not-leak"}
			got := wrapPgErr(pgErr, "Disk", "epd-x")
			if !errors.Is(got, tc.want) {
				t.Fatalf("wrapPgErr(%s): got %v, want sentinel %v", tc.code, got, tc.want)
			}
			if strings.Contains(got.Error(), "raw-pg-detail-must-not-leak") {
				t.Fatalf("wrapPgErr(%s): must not leak raw pg message, got %q", tc.code, got.Error())
			}
		})
	}
}
