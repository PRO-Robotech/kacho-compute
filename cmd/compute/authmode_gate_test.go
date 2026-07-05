// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// allEdgesSecured возвращает config, у которого КАЖДОЕ реально дозваниваемое
// transport-ребро (оба server-listener'а, project/geo peer-рёбра, authz Check,
// register-drainer) несёт enabled per-edge mTLS. Это единственная конфигурация,
// которую production-strict-гейт обязан пропускать.
func allEdgesSecured() config.Config {
	return config.Config{
		AuthMode:                  "production-strict",
		DBSSLMode:                 "verify-full",
		FGARegisterDrainerEnabled: true,
		PublicServerMTLS:          grpcsrv.TLSServer{Enable: true},
		InternalServerMTLS:        grpcsrv.TLSServer{Enable: true},
		IAMProjectMTLS:            grpcclient.TLSClient{Enable: true},
		GeoMTLS:                   grpcclient.TLSClient{Enable: true},
		IAMAuthzMTLS:              grpcclient.TLSClient{Enable: true},
		IAMRegisterMTLS:           grpcclient.TLSClient{Enable: true},
	}
}

// production-strict с legacy IAMTLS=true, но БЕЗ per-edge mTLS обязан ПАДАТЬ:
// мёртвый knob cfg.IAMTLS не отражает реальную защищённость проводов.
func TestValidateAuthMode_ProductionStrict_DeadIAMTLSKnobDoesNotSatisfyGate(t *testing.T) {
	cfg := config.Config{
		AuthMode:                  "production-strict",
		DBSSLMode:                 "verify-full",
		IAMTLS:                    true, // legacy dead knob — must NOT satisfy the gate
		FGARegisterDrainerEnabled: true, // register-drainer edge active
		// all per-edge mTLS disabled (zero-value Enable:false)
	}
	_, err := validateAuthMode(cfg, discardLogger())
	if err == nil {
		t.Fatalf("expected production-strict gate to reject config with all per-edge mTLS disabled (legacy IAMTLS=true must not satisfy it)")
	}
	// error must name the insecure edges, not the dead knob.
	for _, want := range []string{
		"PUBLIC_SERVER_MTLS", "INTERNAL_SERVER_MTLS",
		"IAM_PROJECT_MTLS", "GEO_MTLS", "IAM_AUTHZ_MTLS", "IAM_REGISTER_MTLS",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("gate error must name insecure edge %q; got: %v", want, err)
		}
	}
}

// production-strict со ВСЕМИ per-edge mTLS enabled обязан ПРОХОДИТЬ, даже если
// legacy IAMTLS=false.
func TestValidateAuthMode_ProductionStrict_AllPerEdgeMTLSPasses(t *testing.T) {
	cfg := allEdgesSecured()
	cfg.IAMTLS = false // legacy knob irrelevant now
	prod, err := validateAuthMode(cfg, discardLogger())
	if err != nil {
		t.Fatalf("expected production-strict to pass with all per-edge mTLS enabled; got err: %v", err)
	}
	if !prod {
		t.Errorf("production-strict must report productionMode=true")
	}
}

// Каждое отдельное отключённое ребро должно валить гейт (по одному).
func TestValidateAuthMode_ProductionStrict_EachEdgeRequired(t *testing.T) {
	cases := map[string]func(*config.Config){
		"PUBLIC_SERVER_MTLS":   func(c *config.Config) { c.PublicServerMTLS.Enable = false },
		"INTERNAL_SERVER_MTLS": func(c *config.Config) { c.InternalServerMTLS.Enable = false },
		"IAM_PROJECT_MTLS":     func(c *config.Config) { c.IAMProjectMTLS.Enable = false },
		"GEO_MTLS":             func(c *config.Config) { c.GeoMTLS.Enable = false },
		"IAM_AUTHZ_MTLS":       func(c *config.Config) { c.IAMAuthzMTLS.Enable = false },
		"IAM_REGISTER_MTLS":    func(c *config.Config) { c.IAMRegisterMTLS.Enable = false },
	}
	for edge, disable := range cases {
		t.Run(edge, func(t *testing.T) {
			cfg := allEdgesSecured()
			disable(&cfg)
			_, err := validateAuthMode(cfg, discardLogger())
			if err == nil {
				t.Fatalf("expected gate to reject config with %s disabled", edge)
			}
			if !strings.Contains(err.Error(), edge) {
				t.Errorf("gate error must name disabled edge %q; got: %v", edge, err)
			}
		})
	}
}

// SkipPeerValidation снимает требование на project/geo peer-рёбра (они не
// дозваниваются), но server-listener'ы и authz по-прежнему обязаны быть mTLS.
func TestValidateAuthMode_ProductionStrict_SkipPeerValidationDropsPeerEdges(t *testing.T) {
	cfg := allEdgesSecured()
	cfg.SkipPeerValidation = true
	cfg.IAMProjectMTLS.Enable = false
	cfg.GeoMTLS.Enable = false
	if _, err := validateAuthMode(cfg, discardLogger()); err != nil {
		t.Fatalf("peer edges not dialed under SkipPeerValidation; gate must not require them: %v", err)
	}
}

// AuthZBreakglass снимает требование на authz Check-ребро (interceptor не
// навешивается), но server-listener'ы обязаны быть mTLS.
func TestValidateAuthMode_ProductionStrict_BreakglassDropsAuthzEdge(t *testing.T) {
	cfg := allEdgesSecured()
	cfg.AuthZBreakglass = true
	cfg.IAMAuthzMTLS.Enable = false
	if _, err := validateAuthMode(cfg, discardLogger()); err != nil {
		t.Fatalf("authz edge not dialed under breakglass; gate must not require it: %v", err)
	}
}

// FGARegisterDrainerEnabled=false снимает требование на register-drainer ребро.
func TestValidateAuthMode_ProductionStrict_DrainerDisabledDropsRegisterEdge(t *testing.T) {
	cfg := allEdgesSecured()
	cfg.FGARegisterDrainerEnabled = false
	cfg.IAMRegisterMTLS.Enable = false
	if _, err := validateAuthMode(cfg, discardLogger()); err != nil {
		t.Fatalf("register-drainer disabled; gate must not require IAM_REGISTER_MTLS: %v", err)
	}
}

// DBSSLMode-проверка сохранена.
func TestValidateAuthMode_ProductionStrict_DBSSLModeStillEnforced(t *testing.T) {
	cfg := allEdgesSecured()
	cfg.DBSSLMode = "disable"
	if _, err := validateAuthMode(cfg, discardLogger()); err == nil {
		t.Fatalf("expected gate to reject DBSSLMode=disable in production-strict")
	}
}

// dev / production (не strict) не требуют per-edge mTLS.
func TestValidateAuthMode_DevAndProductionNoTLSGate(t *testing.T) {
	for _, mode := range []string{"dev", "production"} {
		prod, err := validateAuthMode(config.Config{AuthMode: mode}, discardLogger())
		if err != nil {
			t.Fatalf("mode %q must not enforce per-edge mTLS gate; got err: %v", mode, err)
		}
		if mode == "production" && !prod {
			t.Errorf("production must report productionMode=true")
		}
		if mode == "dev" && prod {
			t.Errorf("dev must report productionMode=false")
		}
	}
}
