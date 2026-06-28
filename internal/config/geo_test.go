// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
)

// TestGeo_DefaultAddr — KACHO_COMPUTE_GEO_GRPC_ADDR имеет дефолт на кластерный
// kacho-geo public :9090 (S4: compute валидирует zone_id через kacho-geo).
func TestGeo_DefaultAddr(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD": "x",
	}))
	assert.Equal(t, "kacho-geo.kacho.svc.cluster.local:9090", cfg.GeoGRPCAddr)
}

// TestGeo_AddrOverride — адрес geo конфигурируем через env.
func TestGeo_AddrOverride(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":   "x",
		"KACHO_COMPUTE_GEO_GRPC_ADDR": "localhost:19090",
	}))
	assert.Equal(t, "localhost:19090", cfg.GeoGRPCAddr)
}

// TestGeo_MTLS_DisabledDefaultInsecure — per-edge compute→geo mTLS выключен по
// умолчанию (dev backward-compat, как остальные internal-рёбра compute).
func TestGeo_MTLS_DisabledDefaultInsecure(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD": "x",
	}))
	assert.False(t, cfg.GeoMTLS.Enable, "compute→geo mTLS off by default")
	opt, err := cfg.GeoClientCreds()
	require.NoError(t, err)
	require.NotNil(t, opt)
}

// TestGeo_MTLS_EnabledClientCredsBuild — enable=true с валидным cert-trio →
// client transport creds собираются (mTLS как у других internal-peer'ов compute).
func TestGeo_MTLS_EnabledClientCredsBuild(t *testing.T) {
	certFile, keyFile, caFile := writeTestCert(t)
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":         "x",
		"KACHO_COMPUTE_GEO_MTLS_ENABLE":     "true",
		"KACHO_COMPUTE_GEO_MTLS_CERTFILE":   certFile,
		"KACHO_COMPUTE_GEO_MTLS_KEYFILE":    keyFile,
		"KACHO_COMPUTE_GEO_MTLS_CAFILES":    caFile,
		"KACHO_COMPUTE_GEO_MTLS_SERVERNAME": "kacho-iam.kacho.svc.cluster.local",
	}))
	assert.True(t, cfg.GeoMTLS.Enable)
	opt, err := cfg.GeoClientCreds()
	require.NoError(t, err, "valid cert trio → client creds build")
	require.NotNil(t, opt)
}

// TestGeo_MTLS_FailClosedMissingCA — enable=true без CA → ошибка (fail-closed,
// никогда не silent-insecure-fallback).
func TestGeo_MTLS_FailClosedMissingCA(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":     "x",
		"KACHO_COMPUTE_GEO_MTLS_ENABLE": "true",
		// no CAFILES / SERVERNAME → fail-closed.
	}))
	_, err := cfg.GeoClientCreds()
	require.Error(t, err, "enabled geo mTLS without CA must fail-closed")
}
