package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
)

// SEC-I (sub-phase SEC-I §C) — CLIENT mTLS on the compute→iam read/authz edges.
//
// These mirror the SEC-D register-drainer wiring tests (mtls_test.go) for the two
// remaining plaintext iam-dialing conns: ProjectService.Get (:9090) and the
// per-RPC InternalIAMService.Check + list-filter authorize conn (:9091). Both
// were server-auth-only bool knobs (cfg.IAMTLS / cfg.AuthZIAMTLS) presenting NO
// client-cert; under SEC-H (iam RequireAndVerifyClientCert) those dials fail the
// handshake, so each must present the kacho-compute-client-tls client-cert.

// TestMTLS_SEC_I_C01_IAMReadAuthzDisabledDefaultInsecure — C-01: enable=false
// (default) for both new iam read/authz edges → insecure dial-opt builds, no cert
// files read; zero dev regression (mirror of SEC-D-16).
func TestMTLS_SEC_I_C01_IAMReadAuthzDisabledDefaultInsecure(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD": "x",
	}))
	assert.False(t, cfg.IAMProjectMTLS.Enable, "compute→iam ProjectService.Get mTLS off by default")
	assert.False(t, cfg.IAMAuthzMTLS.Enable, "compute→iam Check/list-filter mTLS off by default")

	optProj, err := cfg.IAMProjectClientCreds()
	require.NoError(t, err)
	require.NotNil(t, optProj)
	optAuthz, err := cfg.IAMAuthzClientCreds()
	require.NoError(t, err)
	require.NotNil(t, optAuthz)
}

// TestMTLS_SEC_I_C02_IAMProjectEnabledClientCredsBuild — C-02: enable=true with a
// valid cert/key/ca trio + ServerName=kacho-iam (public :9090 dial-host) builds
// client transport creds.
func TestMTLS_SEC_I_C02_IAMProjectEnabledClientCredsBuild(t *testing.T) {
	certFile, keyFile, caFile := writeTestCert(t)
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":                 "x",
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_ENABLE":     "true",
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_CERTFILE":   certFile,
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_KEYFILE":    keyFile,
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_CAFILES":    caFile,
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_SERVERNAME": "kacho-iam.kacho.svc.cluster.local",
	}))
	assert.True(t, cfg.IAMProjectMTLS.Enable)
	assert.Equal(t, "kacho-iam.kacho.svc.cluster.local", cfg.IAMProjectMTLS.ServerName)
	opt, err := cfg.IAMProjectClientCreds()
	require.NoError(t, err, "valid cert trio → ProjectService.Get client creds build")
	require.NotNil(t, opt)
}

// TestMTLS_SEC_I_C03_IAMAuthzEnabledClientCredsBuild — C-03: enable=true with a
// valid cert/key/ca trio + ServerName=kacho-iam-internal (internal :9091
// dial-host, Check + list-filter) builds client transport creds.
func TestMTLS_SEC_I_C03_IAMAuthzEnabledClientCredsBuild(t *testing.T) {
	certFile, keyFile, caFile := writeTestCert(t)
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":               "x",
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_ENABLE":     "true",
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_CERTFILE":   certFile,
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_KEYFILE":    keyFile,
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_CAFILES":    caFile,
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_SERVERNAME": "kacho-iam-internal.kacho.svc.cluster.local",
	}))
	assert.True(t, cfg.IAMAuthzMTLS.Enable)
	assert.Equal(t, "kacho-iam-internal.kacho.svc.cluster.local", cfg.IAMAuthzMTLS.ServerName)
	opt, err := cfg.IAMAuthzClientCreds()
	require.NoError(t, err, "valid cert trio → Check/list-filter client creds build")
	require.NotNil(t, opt)
}

// TestMTLS_SEC_I_C07_IAMProjectFailClosedMissingCA — C-07 (mirror A-03): enable=true
// but empty ca_files → error (fail-closed, never silent insecure).
func TestMTLS_SEC_I_C07_IAMProjectFailClosedMissingCA(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":             "x",
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_ENABLE": "true",
		// no CAFILES / SERVERNAME → fail-closed.
	}))
	_, err := cfg.IAMProjectClientCreds()
	require.Error(t, err, "enabled ProjectService.Get mTLS without CA must fail-closed")
}

// TestMTLS_SEC_I_C07_IAMAuthzFailClosedMissingServerName — C-07 (mirror A-04):
// enable=true, valid CA but empty server_name → error (fail-closed).
func TestMTLS_SEC_I_C07_IAMAuthzFailClosedMissingServerName(t *testing.T) {
	_, _, caFile := writeTestCert(t)
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":            "x",
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_ENABLE":  "true",
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_CAFILES": caFile,
		// no SERVERNAME → fail-closed.
	}))
	_, err := cfg.IAMAuthzClientCreds()
	require.Error(t, err, "enabled Check/list-filter mTLS without server_name must fail-closed")
}

// TestMTLS_SEC_I_PerEdgeIndependence — I4: the two new iam read/authz edges and
// the SEC-D register edge resolve to INDEPENDENT env blocks (one process may run
// one edge mTLS and another insecure). Verifies per-edge prefixing (FD-3) — a
// regression here would collapse the edges onto shared env names.
func TestMTLS_SEC_I_PerEdgeIndependence(t *testing.T) {
	certFile, keyFile, caFile := writeTestCert(t)
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD": "x",
		// Only the project edge is on.
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_ENABLE":     "true",
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_CERTFILE":   certFile,
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_KEYFILE":    keyFile,
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_CAFILES":    caFile,
		"KACHO_COMPUTE_IAM_PROJECT_MTLS_SERVERNAME": "kacho-iam.kacho.svc.cluster.local",
	}))
	assert.True(t, cfg.IAMProjectMTLS.Enable, "project edge on")
	assert.False(t, cfg.IAMAuthzMTLS.Enable, "authz edge independently off")
	assert.False(t, cfg.IAMRegisterMTLS.Enable, "register edge independently off")
}
