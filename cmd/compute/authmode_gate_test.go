// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"bytes"
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

// captureLogger — logger, пишущий в buf (для проверки boot-time WARN'ов).
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
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
		AuthZTrustedForwarderSANs: []string{"spiffe://kacho.cloud/ns/kacho-system/sa/kacho-api-gateway"},
		// per-object FGA List-filter active (production fail-closed gate).
		ListFilterEnabled: true,
		AuthZIAMGRPCAddr:  "kacho-iam.kacho.svc.cluster.local:9091",
	}
}

// production-strict БЕЗ единого включённого per-edge mTLS обязан ПАДАТЬ: гейт
// строится исключительно на реально дозваниваемых transport-рёбрах.
func TestValidateAuthMode_ProductionStrict_AllPerEdgeMTLSDisabledFails(t *testing.T) {
	cfg := config.Config{
		AuthMode:                  "production-strict",
		DBSSLMode:                 "verify-full",
		FGARegisterDrainerEnabled: true, // register-drainer edge active
		// all per-edge mTLS disabled (zero-value Enable:false)
	}
	_, err := validateAuthMode(cfg, discardLogger())
	if err == nil {
		t.Fatalf("expected production-strict gate to reject config with all per-edge mTLS disabled")
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

// production-strict со ВСЕМИ per-edge mTLS enabled обязан ПРОХОДИТЬ.
func TestValidateAuthMode_ProductionStrict_AllPerEdgeMTLSPasses(t *testing.T) {
	cfg := allEdgesSecured()
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

// production ОБЯЗАН отказать в старте, если allow-list доверенных forwarder'ов
// (KACHO_COMPUTE_AUTHZ_TRUSTED_FORWARDER_SANS) пуст: с пустым списком
// principalIsTrusted доверяет forwarded x-kacho-principal-* ЛЮБОМУ mTLS-verified
// peer'у — любой sibling с валидным mesh-cert'ом форжит end-user principal и
// авторизуется как жертва (CWE-441/CWE-290 confused deputy → tenant crossing).
// Finding: «Production mode does not require a trusted-forwarder allow-list».
func TestValidateAuthMode_Production_RequiresTrustedForwarders(t *testing.T) {
	cfg := securedProduction()
	cfg.AuthZTrustedForwarderSANs = nil
	_, err := validateAuthMode(cfg, discardLogger())
	if err == nil {
		t.Fatalf("production must reject empty AuthZTrustedForwarderSANs (any mTLS peer trusted as forwarder → subject spoofing)")
	}
	if !strings.Contains(err.Error(), "AUTHZ_TRUSTED_FORWARDER_SANS") {
		t.Errorf("gate error must name the empty forwarder allow-list; got: %v", err)
	}
	// с непустым allow-list — стартует.
	cfg.AuthZTrustedForwarderSANs = []string{"spiffe://kacho.cloud/ns/kacho-system/sa/kacho-api-gateway"}
	if _, err := validateAuthMode(cfg, discardLogger()); err != nil {
		t.Fatalf("production with a non-empty forwarder allow-list must pass; got: %v", err)
	}
}

// production-strict — тот же fail-closed forwarder-гейт.
func TestValidateAuthMode_ProductionStrict_RequiresTrustedForwarders(t *testing.T) {
	cfg := allEdgesSecured()
	cfg.AuthZTrustedForwarderSANs = nil
	_, err := validateAuthMode(cfg, discardLogger())
	if err == nil {
		t.Fatalf("production-strict must reject empty AuthZTrustedForwarderSANs")
	}
	if !strings.Contains(err.Error(), "AUTHZ_TRUSTED_FORWARDER_SANS") {
		t.Errorf("gate error must name the empty forwarder allow-list; got: %v", err)
	}
}

// production ОБЯЗАН отказать в старте, если per-object FGA List-фильтр можно
// молча выключить: KACHO_COMPUTE_LIST_FILTER_ENABLED=false ИЛИ пустой
// KACHO_COMPUTE_AUTHZ_IAM_GRPC_ADDR (→ authzConn nil → handler bypass'ит фильтр).
// Без фильтра principal с project-tier viewer видит ВСЕ Disk/Image/Snapshot/
// Instance проекта, включая объекты без per-object v_get (over-show / BOLA-lite,
// CWE-862 / OWASP A01). Fail-closed зеркалит requireDBSSLMode /
// requireTrustedForwarders.
func TestValidateAuthMode_Production_RequiresListFilter(t *testing.T) {
	// master-switch off → отказ.
	cfg := securedProduction()
	cfg.ListFilterEnabled = false
	if _, err := validateAuthMode(cfg, discardLogger()); err == nil {
		t.Fatalf("production must reject LIST_FILTER_ENABLED=false (public List bypasses per-object FGA filter)")
	} else if !strings.Contains(err.Error(), "LIST_FILTER_ENABLED") {
		t.Errorf("gate error must name the disabled list-filter switch; got: %v", err)
	}

	// authz endpoint unset → authzConn nil → фильтр не строится → отказ.
	cfg = securedProduction()
	cfg.AuthZIAMGRPCAddr = ""
	if _, err := validateAuthMode(cfg, discardLogger()); err == nil {
		t.Fatalf("production must reject empty AUTHZ_IAM_GRPC_ADDR (authzConn nil → List filter disabled)")
	} else if !strings.Contains(err.Error(), "AUTHZ_IAM_GRPC_ADDR") {
		t.Errorf("gate error must name the missing authz endpoint; got: %v", err)
	}

	// enabled + endpoint set → стартует.
	if _, err := validateAuthMode(securedProduction(), discardLogger()); err != nil {
		t.Fatalf("production with list-filter enabled + authz endpoint must pass; got: %v", err)
	}
}

// production-strict — тот же fail-closed list-filter гейт.
func TestValidateAuthMode_ProductionStrict_RequiresListFilter(t *testing.T) {
	cfg := allEdgesSecured()
	cfg.ListFilterEnabled = false
	if _, err := validateAuthMode(cfg, discardLogger()); err == nil {
		t.Fatalf("production-strict must reject LIST_FILTER_ENABLED=false")
	} else if !strings.Contains(err.Error(), "LIST_FILTER_ENABLED") {
		t.Errorf("gate error must name the disabled list-filter switch; got: %v", err)
	}

	cfg = allEdgesSecured()
	cfg.AuthZIAMGRPCAddr = ""
	if _, err := validateAuthMode(cfg, discardLogger()); err == nil {
		t.Fatalf("production-strict must reject empty AUTHZ_IAM_GRPC_ADDR")
	} else if !strings.Contains(err.Error(), "AUTHZ_IAM_GRPC_ADDR") {
		t.Errorf("gate error must name the missing authz endpoint; got: %v", err)
	}
}

// dev не требует ни mTLS, ни SSL (insecure dev-defaults только логируются).
func TestValidateAuthMode_DevNoGate(t *testing.T) {
	prod, err := validateAuthMode(config.Config{AuthMode: "dev"}, discardLogger())
	if err != nil {
		t.Fatalf("dev must not enforce any transport gate; got err: %v", err)
	}
	if prod {
		t.Errorf("dev must report productionMode=false")
	}
}

// securedProduction — минимально-валидный "production": оба server-листенера под
// mTLS + TLS-DB. Peer-рёбра (iam/geo/authz/register) — plaintext (послабление
// plain production относительно production-strict: они mesh-encrypted).
func securedProduction() config.Config {
	return config.Config{
		AuthMode:                  "production",
		DBSSLMode:                 "require",
		PublicServerMTLS:          grpcsrv.TLSServer{Enable: true},
		InternalServerMTLS:        grpcsrv.TLSServer{Enable: true},
		AuthZTrustedForwarderSANs: []string{"spiffe://kacho.cloud/ns/kacho-system/sa/kacho-api-gateway"},
		// per-object FGA List-filter active (production fail-closed gate).
		ListFilterEnabled: true,
		AuthZIAMGRPCAddr:  "kacho-iam.kacho.svc.cluster.local:9091",
	}
}

// production ОБЯЗАН отказать в старте с plaintext-листенерами: forwarded
// principal доверяется на plaintext → subject spoofing / tenant crossing
// (CWE-290). Раньше это гейтилось только в production-strict. Finding «Non-strict
// production AuthMode leaves listeners plaintext … subject spoofing».
func TestValidateAuthMode_Production_RequiresListenerMTLS(t *testing.T) {
	// оба листенера plaintext (+ валидный DBSSL, чтобы изолировать listener-гейт).
	cfg := config.Config{AuthMode: "production", DBSSLMode: "require"}
	_, err := validateAuthMode(cfg, discardLogger())
	if err == nil {
		t.Fatalf("production must reject plaintext listeners (forwarded principal spoofing)")
	}
	for _, want := range []string{"PUBLIC_SERVER_MTLS_ENABLE", "INTERNAL_SERVER_MTLS_ENABLE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("gate error must name insecure listener %q; got: %v", want, err)
		}
	}

	// по одному листенеру — тоже отказ.
	for _, disable := range []func(*config.Config){
		func(c *config.Config) { c.PublicServerMTLS.Enable = false },
		func(c *config.Config) { c.InternalServerMTLS.Enable = false },
	} {
		c := securedProduction()
		disable(&c)
		if _, err := validateAuthMode(c, discardLogger()); err == nil {
			t.Errorf("production must reject a single plaintext listener")
		}
	}
}

// production ОБЯЗАН отказать при sslmode=disable — DB-креды/строки идут открытым
// текстом (CWE-319). Раньше SSL-проверка была только в production-strict. Finding
// «DB connection allows sslmode=disable outside production-strict».
func TestValidateAuthMode_Production_RequiresDBSSL(t *testing.T) {
	for _, bad := range []string{"", "disable"} {
		cfg := securedProduction()
		cfg.DBSSLMode = bad
		if _, err := validateAuthMode(cfg, discardLogger()); err == nil {
			t.Errorf("production must reject KACHO_COMPUTE_DB_SSLMODE=%q", bad)
		}
	}
	// require/verify-ca/verify-full — принимаются.
	for _, ok := range []string{"require", "verify-ca", "verify-full"} {
		cfg := securedProduction()
		cfg.DBSSLMode = ok
		if _, err := validateAuthMode(cfg, discardLogger()); err != nil {
			t.Errorf("production must accept DBSSLMode=%q; got err: %v", ok, err)
		}
	}
}

// breakglass в production — намеренный emergency-escape (зеркалит kacho-vpc:
// warn-not-reject; существующий TestValidateAuthMode_ProductionStrict_
// BreakglassDropsAuthzEdge подтверждает, что gate его пропускает). НО он не должен
// быть МОЛЧАЛИВЫМ: boot ОБЯЗАН громко предупредить, что per-RPC authz Check
// целиком обойдён. Finding r9b-1: «production gate silently disables all authz».
func TestValidateAuthMode_Production_BreakglassEmitsLoudWarn(t *testing.T) {
	for _, mode := range []string{"production", "production-strict"} {
		t.Run(mode, func(t *testing.T) {
			var cfg config.Config
			if mode == "production-strict" {
				cfg = allEdgesSecured()
			} else {
				cfg = securedProduction()
			}
			cfg.AuthMode = mode
			cfg.AuthZBreakglass = true
			// production-strict иначе потребует IAM_AUTHZ_MTLS; breakglass снимает это.
			cfg.IAMAuthzMTLS.Enable = false

			var buf bytes.Buffer
			prod, err := validateAuthMode(cfg, captureLogger(&buf))
			if err != nil {
				t.Fatalf("breakglass in %s must NOT reject boot (emergency escape, mirrors kacho-vpc); got err: %v", mode, err)
			}
			if !prod {
				t.Errorf("%s must report productionMode=true", mode)
			}
			got := strings.ToLower(buf.String())
			if !strings.Contains(got, "breakglass") {
				t.Errorf("%s + breakglass must emit a boot WARN naming breakglass; got log: %q", mode, buf.String())
			}
			if !strings.Contains(got, "bypass") {
				t.Errorf("%s + breakglass WARN must state that authz Check is BYPASSED; got log: %q", mode, buf.String())
			}
		})
	}
}

// Обратная сторона: без breakglass production НЕ должен эмитить breakglass-WARN
// (иначе алерт-шум / притупление внимания к реальному emergency-обходу).
func TestValidateAuthMode_Production_NoBreakglassNoWarn(t *testing.T) {
	var buf bytes.Buffer
	if _, err := validateAuthMode(securedProduction(), captureLogger(&buf)); err != nil {
		t.Fatalf("secured production must pass; got: %v", err)
	}
	if strings.Contains(strings.ToLower(buf.String()), "breakglass") {
		t.Errorf("production WITHOUT breakglass must not emit a breakglass WARN; got log: %q", buf.String())
	}
}

// production с mTLS-листенерами + TLS-DB, но plaintext peer-рёбрами — ПРОХОДИТ:
// это осознанная разница ladder'а plain production vs production-strict (peer
// строгий mTLS требует именно strict).
func TestValidateAuthMode_Production_PeerEdgesNotRequired(t *testing.T) {
	cfg := securedProduction()
	// peer-рёбра явно plaintext + активны.
	cfg.FGARegisterDrainerEnabled = true
	prod, err := validateAuthMode(cfg, discardLogger())
	if err != nil {
		t.Fatalf("plain production must not require peer-edge mTLS; got err: %v", err)
	}
	if !prod {
		t.Errorf("production must report productionMode=true")
	}
}
