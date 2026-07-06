// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
)

// writeCmdTestCert generates a throwaway self-signed cert+key+CA PEM trio for the
// cmd-level mTLS seam tests (no real PKI). Returns cert, key, ca paths.
func writeCmdTestCert(t *testing.T) (certFile, keyFile, caFile string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kacho-compute-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              []string{"kacho-iam-internal.kacho.svc.cluster.local"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	caFile = filepath.Join(dir, "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(caFile, certPEM, 0o600))
	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile, caFile
}

// loadCfg сеттит env, ограниченные тестом (t.Setenv), и грузит Config тем же
// путём, что и прод config.Load — тест-хелпер не попадает в прод-бинарь.
func loadCfg(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := config.Load()
	require.NoError(t, err)
	return cfg
}

// CLIENT mTLS on the compute→iam read/authz dial seam: it must thread a per-edge
// grpcclient.TLSClient (client-cert mTLS) rather than the server-auth-only bool.
// These guard the seam: peerDialOptsCreds(creds, idle) builds the dial-opts from
// corelib-resolved transport creds, and the iam ProjectService.Get and
// Check/list-filter conns resolve their creds from the TLSClient edges.

// TestSEC_I_PeerDialOptsCreds_KeepsKeepalive — the creds-aware seam (used for the
// iam read/authz edges) keeps the keepalive dial-option and presents the
// passed transport-creds. Mirrors dialpeer_test.go's keepalive guard for the new seam.
func TestSEC_I_PeerDialOptsCreds_KeepsKeepalive(t *testing.T) {
	creds, err := grpcclient.TLSClientTransportCreds(grpcclient.TLSClient{Enable: false})
	require.NoError(t, err)
	opts := peerDialOptsCreds(creds, true)
	require.GreaterOrEqual(t, len(opts), 2, "creds-aware seam = [creds-opt, keepalive-opt]")
	require.True(t, hasKeepaliveOpt(opts), "creds-aware seam keeps keepalive")
}

// TestSEC_I_IAMProjectDialCreds_Insecure_Default — ProjectService.Get edge:
// enable=false (default) → corelib builds insecure creds (zero dev regression), the
// dial-opts build without reading cert files.
func TestSEC_I_IAMProjectDialCreds_Insecure_Default(t *testing.T) {
	cfg := loadCfg(t, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD": "x",
	})
	creds, err := grpcclient.TLSClientTransportCreds(cfg.IAMProjectMTLS)
	require.NoError(t, err)
	require.NotNil(t, creds)
	require.GreaterOrEqual(t, len(peerDialOptsCreds(creds, false)), 2)
}

// TestSEC_I_IAMAuthzDialCreds_ClientCert_Enabled — Check/list-filter edge:
// enable=true with a valid trio → corelib builds client-cert mTLS creds; the
// authz conn presents the kacho-compute-client-tls cert (handshake-level behavior
// is covered by corelib bufconn tests).
func TestSEC_I_IAMAuthzDialCreds_ClientCert_Enabled(t *testing.T) {
	certFile, keyFile, caFile := writeCmdTestCert(t)
	cfg := loadCfg(t, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":               "x",
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_ENABLE":     "true",
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_CERTFILE":   certFile,
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_KEYFILE":    keyFile,
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_CAFILES":    caFile,
		"KACHO_COMPUTE_IAM_AUTHZ_MTLS_SERVERNAME": "kacho-iam-internal.kacho.svc.cluster.local",
	})
	creds, err := grpcclient.TLSClientTransportCreds(cfg.IAMAuthzMTLS)
	require.NoError(t, err, "valid trio → client-cert creds")
	require.NotNil(t, creds)
	require.Equal(t, "tls", creds.Info().SecurityProtocol, "authz edge dials over TLS, not insecure")
	require.GreaterOrEqual(t, len(peerDialOptsCreds(creds, true)), 2)
}
