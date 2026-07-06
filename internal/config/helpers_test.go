// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
)

// loadCfg сеттит env-переменные, ограниченные текущим тестом (t.Setenv —
// авто-restore на t.Cleanup, паника при parallel-misuse, без discarded-ошибок), и
// грузит Config тем же путём, что и прод config.Load. Тест-хелпер живёт в
// _test.go: он не попадает в прод-бинарь (в отличие от прежнего экспортированного
// config.LoadInto, мутировавшего process-global env).
func loadCfg(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := config.Load()
	require.NoError(t, err)
	return cfg
}
