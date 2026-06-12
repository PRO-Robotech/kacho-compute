package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
)

// SEC-M (sub-phase SEC-M §B) — the compute→vpc resource-creation edge must thread a
// per-edge grpcclient.TLSClient (client-cert mTLS) into BOTH vpc dials:
//   - vpcConn         (:9090, Subnet/SecurityGroup/Address.Get — NIC-spec validation)
//   - vpcInternalConn (:9091, InternalAddressService — one-to-one-NAT IPAM)
// rather than the server-auth-only bools (cfg.VPCTLS / cfg.VPCInternalTLS). Mirror of
// the already-shipped vpc→compute branch (mtlsCfg.ComputeMTLS.Enable) and the SEC-I
// iam read/authz edges. Both vpc-listeners share one Service-host `vpc` → one
// cfg.VPCMTLS / one ServerName covers both ports (M6, B-04; contrast iam SEC-I split).

// TestSECM_VPCDialCreds_Insecure_Default — both vpc edges: enable=false (default) →
// corelib builds insecure creds (zero dev regression); the dial-opts build without
// reading cert files. Mirrors TestSEC_I_IAMProjectDialCreds_Insecure_Default.
func TestSECM_VPCDialCreds_Insecure_Default(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD": "x",
	}))
	require.False(t, cfg.VPCMTLS.Enable, "vpc edge default is insecure (dev backward-compat)")
	creds, err := grpcclient.TLSClientTransportCreds(cfg.VPCMTLS)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.GreaterOrEqual(t, len(peerDialOptsCreds(creds, false)), 2)
}

// TestSECM_VPCDialCreds_ClientCert_Enabled — both vpc edges: enable=true with a valid
// trio → corelib builds client-cert mTLS creds; the vpc conns present the
// kacho-compute-client-tls cert (handshake-level behaviour covered by corelib SEC-B
// bufconn tests). ServerName = vpc dial-host (∈ vpc server-SAN, M6). B-02/B-03.
func TestSECM_VPCDialCreds_ClientCert_Enabled(t *testing.T) {
	certFile, keyFile, caFile := writeCmdTestCert(t)
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":         "x",
		"KACHO_COMPUTE_VPC_MTLS_ENABLE":     "true",
		"KACHO_COMPUTE_VPC_MTLS_CERTFILE":   certFile,
		"KACHO_COMPUTE_VPC_MTLS_KEYFILE":    keyFile,
		"KACHO_COMPUTE_VPC_MTLS_CAFILES":    caFile,
		"KACHO_COMPUTE_VPC_MTLS_SERVERNAME": "vpc.kacho.svc.cluster.local",
	}))
	require.True(t, cfg.VPCMTLS.Enable)
	creds, err := grpcclient.TLSClientTransportCreds(cfg.VPCMTLS)
	require.NoError(t, err, "valid trio → client-cert creds")
	require.NotNil(t, creds)
	require.Equal(t, "tls", creds.Info().SecurityProtocol, "vpc edge dials over TLS, not insecure")
	require.GreaterOrEqual(t, len(peerDialOptsCreds(creds, false)), 2)
}

// TestSECM_VPCMTLS_FailClosed_BadConfig — B-07 (mirror A-03/A-04/A-05): enable=true
// with empty ca_files / empty server_name / unreadable cert → creds build fails
// (fail-closed, no silent insecure-fallback). Composition root surfaces this as a
// startup error before Serve.
func TestSECM_VPCMTLS_FailClosed_BadConfig(t *testing.T) {
	certFile, keyFile, caFile := writeCmdTestCert(t)
	cases := map[string]map[string]string{
		"empty ca_files": {
			"KACHO_COMPUTE_VPC_MTLS_ENABLE":     "true",
			"KACHO_COMPUTE_VPC_MTLS_CERTFILE":   certFile,
			"KACHO_COMPUTE_VPC_MTLS_KEYFILE":    keyFile,
			"KACHO_COMPUTE_VPC_MTLS_SERVERNAME": "vpc.kacho.svc.cluster.local",
		},
		"empty server_name": {
			"KACHO_COMPUTE_VPC_MTLS_ENABLE":   "true",
			"KACHO_COMPUTE_VPC_MTLS_CERTFILE": certFile,
			"KACHO_COMPUTE_VPC_MTLS_KEYFILE":  keyFile,
			"KACHO_COMPUTE_VPC_MTLS_CAFILES":  caFile,
		},
		"unreadable cert": {
			"KACHO_COMPUTE_VPC_MTLS_ENABLE":     "true",
			"KACHO_COMPUTE_VPC_MTLS_CERTFILE":   "/nonexistent/tls.crt",
			"KACHO_COMPUTE_VPC_MTLS_KEYFILE":    "/nonexistent/tls.key",
			"KACHO_COMPUTE_VPC_MTLS_CAFILES":    caFile,
			"KACHO_COMPUTE_VPC_MTLS_SERVERNAME": "vpc.kacho.svc.cluster.local",
		},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			full := map[string]string{"KACHO_COMPUTE_DB_PASSWORD": "x"}
			for k, v := range env {
				full[k] = v
			}
			var cfg config.Config
			require.NoError(t, config.LoadInto(&cfg, full))
			_, err := cfg.VPCClientCreds()
			require.Error(t, err, "fail-closed: enable=true + bad trio must error, not silent insecure")
		})
	}
}

// TestSECM_CompletenessGuard_BothVPCDialsThreadClientCreds — SEC-M-07 static
// completeness gate (per-service, factual conn set). The composition root MUST thread
// per-edge client-cert mTLS creds (mirror of the SEC-D vpc→compute / SEC-I iam branch)
// into BOTH vpc dials: vpcConn (NIC-spec, :9090) and vpcInternalConn (IPAM, :9091). If
// either is left server-auth-only/plaintext, once kacho-vpc runs RequireAndVerifyClientCert
// (SEC-H-family) the handshake fails (code 14) → Instance.Create fails (B-05). This guard
// forbids that regression by asserting both dials branch on cfg.VPCMTLS.Enable and
// consult the VPCClientCreds mTLS helper.
func TestSECM_CompletenessGuard_BothVPCDialsThreadClientCreds(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	main := string(src)

	for _, want := range []string{
		// Both vpc conns gate on the per-edge mTLS enable flag.
		"cfg.VPCMTLS.Enable",
		// Both vpc conns resolve their client-cert creds from the per-edge cfg.VPCMTLS
		// TLSClient via the corelib helper (same seam the iam edges use, OQ-2).
		"grpcclient.TLSClientTransportCreds(cfg.VPCMTLS)",
		// Both vpc dials go through the shared creds-aware seam (dialPeerCreds), the
		// same path that carries the SEC-I iam creds (B-06: one seam, not duplicated).
		"dialPeerCreds(",
	} {
		require.Contains(t, main, want,
			"composition root must thread client-cert mTLS into both compute→vpc dials (SEC-M-07); missing %q", want)
	}

	// Both vpc dials must route through the single mTLS-aware helper dialVPCPeer
	// (one branch covers vpcConn :9090 and vpcInternalConn :9091 — one cfg.VPCMTLS,
	// one ServerName, B-04) rather than calling the legacy plaintext dialPeer directly.
	// Two call-sites consume the helper (the func definition uses "func dialVPCPeer(").
	require.Equal(t, 2, strings.Count(main, "dialVPCPeer(cfg,"),
		"both vpcConn (:9090) and vpcInternalConn (:9091) must dial via the mTLS-aware dialVPCPeer helper")

	// The mTLS branch must guard (precede) the legacy plaintext bool path inside the
	// helper — otherwise the insecure dialPeer(..., legacyTLS, ...) would run
	// unconditionally and the edge would stay plaintext under vpc server mTLS (B-05).
	require.Less(t,
		strings.Index(main, "cfg.VPCMTLS.Enable"),
		strings.LastIndex(main, "return dialPeer(addr, legacyTLS, false)"),
		"cfg.VPCMTLS.Enable mTLS branch must guard the vpc insecure/server-auth fallback")

	// iam read/authz edges (SEC-I) must not be regressed by the seam refactor (B-06).
	// compute resolves iam creds through the same TLSClientTransportCreds seam as the
	// new vpc edge (NOT the vpc-side *ClientCreds() DialOption helpers); these anchors
	// pin the SEC-I iam ProjectService.Get and Check/list-filter edges intact.
	require.Contains(t, main, "grpcclient.TLSClientTransportCreds(cfg.IAMProjectMTLS)",
		"SEC-M seam refactor must not regress the SEC-I iam ProjectService.Get edge")
	require.Contains(t, main, "grpcclient.TLSClientTransportCreds(cfg.IAMAuthzMTLS)",
		"SEC-M seam refactor must not regress the SEC-I iam Check/list-filter edge")
}
