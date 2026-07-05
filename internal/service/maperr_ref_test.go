// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMapRefErr_NotFound_Preserved — настоящий not-found ссылочного ресурса
// (repo вернул ErrNotFound) → codes.NotFound с детерминированным текстом
// "<Resource> <id> not found" (контракт lookup сохранён).
func TestMapRefErr_NotFound_Preserved(t *testing.T) {
	err := mapRefErr(fmt.Errorf("%w: Image img-1 not found", ErrNotFound), "Image", "img-1")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.NotFound, st.Code())
	require.Equal(t, "Image img-1 not found", st.Message())
}

// TestMapRefErr_Transient_NotMaskedAsNotFound — транзиентный/internal сбой БД во
// время lookup НЕ маскируется под перманентный NotFound: ErrInternal →
// codes.Internal "internal database error" (клиент видит retryable-условие, а не
// ложное «ресурс не существует»; CWE-388, регресс 3-го аудита).
func TestMapRefErr_Transient_NotMaskedAsNotFound(t *testing.T) {
	err := mapRefErr(ErrInternal, "Image", "img-1")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Internal, st.Code())
	require.NotEqual(t, codes.NotFound, st.Code())
}

// TestMapRefErr_RawError_NoLeakInternal — сырой (не-sentinel) pgx-error → Internal
// без leak'а текста, не NotFound.
func TestMapRefErr_RawError_NoLeakInternal(t *testing.T) {
	err := mapRefErr(errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"), "Disk", "disk-9")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Internal, st.Code())
	require.Equal(t, "internal database error", st.Message())
}

// TestMapRefErr_Nil — nil-вход даёт nil.
func TestMapRefErr_Nil(t *testing.T) {
	require.NoError(t, mapRefErr(nil, "Image", "img-1"))
}
