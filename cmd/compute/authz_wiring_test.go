// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/authz"

	"github.com/PRO-Robotech/kacho-compute/internal/check"
)

// authz_wiring_test.go — fail-closed boot guard.
//
// SECURITY (audit P0): в production-strict kacho-compute НЕ обязан иметь
// AuthZIAMGRPCAddr — прежняя ветка ErrIAMConnNotConfigured логировала Warn и
// стартовала БЕЗ per-RPC FGA Check и list-filter. Любой запрос проходил без
// авторизации. Fix зеркалит kacho-vpc/cmd/vpc/authz_wiring.go::authzWiringDecision:
// в production отсутствие authz-interceptor'а — ФАТАЛЬНО, в dev — graceful continue.

// production + interceptor отсутствует (ErrIAMConnNotConfigured) → фатальная ошибка.
//
// RED-демонстрация: до фикса функции authzWiringDecision нет, а runServe просто
// логировал Warn и продолжал без Check.
func TestAuthzWiringDecision_Production_Absent_Fatal(t *testing.T) {
	intr, err := authzWiringDecision(true, nil, check.ErrIAMConnNotConfigured)
	require.Error(t, err)
	require.Nil(t, intr)
	require.Contains(t, err.Error(), "production mode requires authz interceptor")
}

// dev + interceptor отсутствует → (nil, nil) continue (dev backward-compat).
func TestAuthzWiringDecision_Dev_Absent_Continue(t *testing.T) {
	intr, err := authzWiringDecision(false, nil, check.ErrIAMConnNotConfigured)
	require.NoError(t, err)
	require.Nil(t, intr, "dev продолжает без authz-interceptor'а")
}

// happy: interceptor собран → возвращается для навешивания на обе цепочки.
func TestAuthzWiringDecision_Present_Attach(t *testing.T) {
	got := &authz.Interceptor{}
	intr, err := authzWiringDecision(true, got, nil)
	require.NoError(t, err)
	require.Same(t, got, intr)
}

// прочая build-ошибка пробрасывается как есть (не маскируется и не fatal'ит по
// production-ветке).
func TestAuthzWiringDecision_OtherError_Propagated(t *testing.T) {
	sentinel := errors.New("boom")
	intr, err := authzWiringDecision(true, nil, sentinel)
	require.ErrorIs(t, err, sentinel)
	require.Nil(t, intr)
}

// TestMain_WiresAuthzWiringDecision — source-level guard: runServe ДОЛЖЕН прогонять
// результат check.NewInterceptor через authzWiringDecision(productionMode, …),
// чтобы production fail-closed реально применялся (а не оставался мёртвой функцией).
//
// RED-демонстрация: оставить в main.go прежний inline-switch без authzWiringDecision
// → этот тест падает.
func TestMain_WiresAuthzWiringDecision(t *testing.T) {
	b, err := os.ReadFile("main.go")
	require.NoError(t, err)
	src := string(b)
	require.Contains(t, src, "authzWiringDecision(productionMode,",
		"runServe должен применять fail-closed решение authzWiringDecision к результату check.NewInterceptor")
}
