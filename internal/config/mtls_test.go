package config_test

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/config"
)

// writeTestCert generates a throwaway self-signed cert+key+CA PEM trio for the
// mTLS-wiring tests (no real PKI — that's SEC-F). Returns cert, key, ca paths.
func writeTestCert(t *testing.T) (certFile, keyFile, caFile string) {
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
		DNSNames:              []string{"kacho-iam.kacho.svc.cluster.local"},
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

// TestMTLS_SEC_D_16_DisabledDefaultInsecure — SEC-D-16: enable=false (default) →
// dial opts build insecure; backward-compat, no cert files read.
func TestMTLS_SEC_D_16_DisabledDefaultInsecure(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD": "x",
	}))
	assert.False(t, cfg.IAMRegisterMTLS.Enable, "register→iam mTLS off by default")
	assert.False(t, cfg.VPCMTLS.Enable, "compute→vpc mTLS off by default")

	// dial opt builds without cert files when disabled.
	opt, err := cfg.IAMRegisterClientCreds()
	require.NoError(t, err)
	require.NotNil(t, opt)
}

// TestMTLS_SEC_D_17_EnabledClientCredsBuild — SEC-D-17/18/19 (compute wiring):
// enable=true with a valid cert/key/ca trio builds client transport creds
// (handshake itself is covered by corelib SEC-B bufconn tests).
func TestMTLS_SEC_D_17_EnabledClientCredsBuild(t *testing.T) {
	certFile, keyFile, caFile := writeTestCert(t)
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":                  "x",
		"KACHO_COMPUTE_IAM_REGISTER_MTLS_ENABLE":     "true",
		"KACHO_COMPUTE_IAM_REGISTER_MTLS_CERTFILE":   certFile,
		"KACHO_COMPUTE_IAM_REGISTER_MTLS_KEYFILE":    keyFile,
		"KACHO_COMPUTE_IAM_REGISTER_MTLS_CAFILES":    caFile,
		"KACHO_COMPUTE_IAM_REGISTER_MTLS_SERVERNAME": "kacho-iam.kacho.svc.cluster.local",
	}))
	assert.True(t, cfg.IAMRegisterMTLS.Enable)
	opt, err := cfg.IAMRegisterClientCreds()
	require.NoError(t, err, "valid cert trio → client creds build")
	require.NotNil(t, opt)
}

// TestMTLS_SEC_D_FailClosedMissingCA — enable=true but empty ca_files → error
// (fail-closed, never silent insecure fallback).
func TestMTLS_SEC_D_FailClosedMissingCA(t *testing.T) {
	var cfg config.Config
	require.NoError(t, config.LoadInto(&cfg, map[string]string{
		"KACHO_COMPUTE_DB_PASSWORD":              "x",
		"KACHO_COMPUTE_IAM_REGISTER_MTLS_ENABLE": "true",
		// no CAFILES / SERVERNAME → fail-closed.
	}))
	_, err := cfg.IAMRegisterClientCreds()
	require.Error(t, err, "enabled mTLS without CA must fail-closed")
}
