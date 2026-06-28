// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package clients_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSEC_D_07_NoDirectFGAInClients — structural gate: there must be NO "openfga"
// reference anywhere under internal/clients/ (the direct FGA HTTP write client is
// gone; compute reaches FGA only through kacho-iam).
func TestSEC_D_07_NoDirectFGAInClients(t *testing.T) {
	dir := "." // internal/clients
	var offenders []string
	err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// This test file itself names "openfga" in comments/strings — skip it.
		if strings.HasSuffix(path, "no_direct_fga_test.go") {
			return nil
		}
		if strings.Contains(strings.ToLower(string(b)), "openfga") {
			offenders = append(offenders, path)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders, "no openfga reference allowed in internal/clients/ (SEC-D-07)")

	// openfga_write_client.go must not exist.
	_, statErr := os.Stat("openfga_write_client.go")
	assert.True(t, os.IsNotExist(statErr), "internal/clients/openfga_write_client.go must be removed")
}
