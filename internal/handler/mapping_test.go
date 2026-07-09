// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// TestOperationToProto_TruncatesTimestamps локает конвенцию Kachō
// (api-conventions.md): timestamp в proto-ответе усекается до секунд — на КАЖДОМ
// ресурсе И на Operation-envelope. Микросекунды из БД не текут на wire.
func TestOperationToProto_TruncatesTimestamps(t *testing.T) {
	created := time.Date(2026, 7, 9, 12, 30, 45, 123456789, time.UTC)
	modified := time.Date(2026, 7, 9, 12, 31, 0, 987654321, time.UTC)
	op := &operations.Operation{
		ID:         "epd00000000000000000",
		CreatedAt:  created,
		ModifiedAt: modified,
	}

	p := operationToProto(op)

	assert.Zero(t, p.GetCreatedAt().AsTime().Nanosecond(),
		"Operation.created_at должен быть усечён до секунд")
	assert.Equal(t, created.Truncate(time.Second).UTC(), p.GetCreatedAt().AsTime().UTC())

	assert.Zero(t, p.GetModifiedAt().AsTime().Nanosecond(),
		"Operation.modified_at должен быть усечён до секунд")
	assert.Equal(t, modified.Truncate(time.Second).UTC(), p.GetModifiedAt().AsTime().UTC())
}
