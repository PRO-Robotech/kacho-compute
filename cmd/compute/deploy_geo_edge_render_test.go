package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// kacho-geo epic (Stage S4 deploy-wiring) — render-guard for the compute→geo edge in
// the Helm chart. The Go composition root already dials kacho-geo via cfg.GeoGRPCAddr +
// cfg.GeoMTLS (geo.v1.ZoneService.Get, public :9090, Instance.zone_id validation), but
// the deploy chart did NOT emit the geo env, so a deployed compute could not present its
// client cert to geo (geo runs server-mTLS on :9090 in dev) → handshake fails.
//
// These guards mirror the static completeness-guard idiom used for the compute→vpc edge
// (cmd/compute/dialpeer_mtls_vpc_test.go::TestSECM_CompletenessGuard...). They assert the
// chart template emits the geo env block exactly the way the vpc/iam edges already do:
//   - KACHO_COMPUTE_GEO_GRPC_ADDR (always, from .Values.geoAddr)
//   - KACHO_COMPUTE_GEO_MTLS_{ENABLE,CERTFILE,KEYFILE,CAFILES,SERVERNAME}, gated on
//     .Values.mtls.edges.geo (per-edge opt-in; default off → insecure dev back-compat).
//
// When the `helm` binary is available the guard additionally renders the chart with
// mtls.edges.geo=true and asserts the geo env appears in the actual rendered Deployment
// (true render-guard, CI path). Locally without helm it falls back to a chart-source
// assertion (same source-text idiom as the vpc completeness guard) so the RED→GREEN
// pair is deterministic regardless of helm presence.

// chartDir returns the absolute path to the compute Helm chart (deploy/) relative to
// the cmd/compute test working directory (repo-root/cmd/compute → repo-root/deploy).
func chartDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", "deploy"))
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(d, "Chart.yaml"))
	require.NoError(t, err, "deploy/Chart.yaml must exist at %s", d)
	require.False(t, info.IsDir())
	return d
}

// readDeploymentTemplate reads deploy/templates/deployment.yaml source text.
func readDeploymentTemplate(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(chartDir(t), "templates", "deployment.yaml"))
	require.NoError(t, err)
	return string(b)
}

// readValues reads deploy/values.yaml source text.
func readValues(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(chartDir(t), "values.yaml"))
	require.NoError(t, err)
	return string(b)
}

// TestGeoEdge_Values_DeclareGeoAddrAndPerEdgeMTLS — values.yaml must declare the geo
// dial address (default kacho-geo public :9090) plus the per-edge mTLS toggle/serverName,
// mirroring the existing vpc/iam edge keys (mtls.edges.<edge> + mtls.serverName.<edge>).
func TestGeoEdge_Values_DeclareGeoAddrAndPerEdgeMTLS(t *testing.T) {
	v := readValues(t)
	require.Contains(t, v, "geoAddr:",
		"values.yaml must declare geoAddr (compute→geo ZoneService.Get dial host)")
	require.Contains(t, v, "kacho-geo.kacho.svc.cluster.local:9090",
		"geoAddr default must be the kacho-geo public :9090 dial host (config.go GeoGRPCAddr default)")
	require.Regexp(t, `geo:\s*false`, v,
		"mtls.edges.geo must default to false (per-edge opt-in, dev insecure back-compat)")
	require.Contains(t, v, "geo: kacho-geo.kacho.svc.cluster.local",
		"mtls.serverName.geo must be the kacho-geo dial host (∈ geo server-cert SAN)")
}

// TestGeoEdge_Deployment_EmitsGeoAddr — the rendered Deployment must always carry the
// geo dial address env, exactly like KACHO_COMPUTE_VPC_GRPC_ADDR.
func TestGeoEdge_Deployment_EmitsGeoAddr(t *testing.T) {
	tpl := readDeploymentTemplate(t)
	require.Contains(t, tpl, "KACHO_COMPUTE_GEO_GRPC_ADDR",
		"deployment.yaml must emit KACHO_COMPUTE_GEO_GRPC_ADDR (config.go cfg.GeoGRPCAddr)")
	require.Contains(t, tpl, ".Values.geoAddr",
		"KACHO_COMPUTE_GEO_GRPC_ADDR must source from .Values.geoAddr (mirror vpcAddr)")
}

// TestGeoEdge_Deployment_EmitsPerEdgeMTLS — the geo client-cert mTLS env block must be
// present, gated on .Values.mtls.edges.geo, using the same cert paths and serverName
// seam as the compute→vpc edge (KACHO_COMPUTE_GEO_MTLS_* from envconfig tag GEO_MTLS).
func TestGeoEdge_Deployment_EmitsPerEdgeMTLS(t *testing.T) {
	tpl := readDeploymentTemplate(t)
	require.Contains(t, tpl, ".Values.mtls.edges.geo",
		"geo mTLS env must be gated on .Values.mtls.edges.geo (per-edge opt-in)")
	for _, want := range []string{
		"KACHO_COMPUTE_GEO_MTLS_ENABLE",
		"KACHO_COMPUTE_GEO_MTLS_CERTFILE",
		"KACHO_COMPUTE_GEO_MTLS_KEYFILE",
		"KACHO_COMPUTE_GEO_MTLS_CAFILES",
		"KACHO_COMPUTE_GEO_MTLS_SERVERNAME",
	} {
		require.Contains(t, tpl, want,
			"deployment.yaml must emit %s for the compute→geo edge (envconfig GEO_MTLS)", want)
	}
	require.Contains(t, tpl, ".Values.mtls.serverName.geo",
		"geo mTLS SERVERNAME must source from .Values.mtls.serverName.geo (mirror vpc)")
}

// TestGeoEdge_HelmRender_GeoEnvPresent — true render-guard: when `helm` is on PATH,
// render the chart with mtls.enable=true + mtls.edges.geo=true and assert the rendered
// compute Deployment contains the geo dial addr and the geo mTLS enable env. Skipped if
// helm is absent (local dev); the source guards above still hold deterministically.
func TestGeoEdge_HelmRender_GeoEnvPresent(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not on PATH — chart-source guards cover the render assertion locally")
	}
	dir := chartDir(t)
	out, err := exec.Command(helm, "template", dir,
		"--set", "mtls.enable=true",
		"--set", "mtls.edges.geo=true",
	).CombinedOutput()
	require.NoError(t, err, "helm template must render cleanly:\n%s", string(out))
	rendered := string(out)

	require.Contains(t, rendered, "KACHO_COMPUTE_GEO_GRPC_ADDR",
		"rendered Deployment must carry the geo dial addr env")
	require.Contains(t, rendered, "kacho-geo.kacho.svc.cluster.local:9090",
		"rendered geo dial addr must be the kacho-geo public :9090 host")
	require.Contains(t, rendered, "KACHO_COMPUTE_GEO_MTLS_ENABLE",
		"rendered Deployment must carry the geo mTLS enable env when mtls.edges.geo=true")

	// Sanity: the GEO_MTLS_ENABLE value must be the literal "true" near its name.
	idx := strings.Index(rendered, "KACHO_COMPUTE_GEO_MTLS_ENABLE")
	require.GreaterOrEqual(t, idx, 0)
	window := rendered[idx:min(idx+120, len(rendered))]
	require.Contains(t, window, "true",
		"KACHO_COMPUTE_GEO_MTLS_ENABLE must render value \"true\" under mtls.edges.geo=true")
}
