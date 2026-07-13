// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInstance_Validate_CPUGuaranteePercent — self-validating domain: cpu_guarantee_percent
// валиден в [0,100]. Границы 0/100 проходят; -1 и 101 → ошибка инварианта.
func TestInstance_Validate_CPUGuaranteePercent(t *testing.T) {
	for _, v := range []int32{0, 1, 50, 100} {
		in := &Instance{CPUGuaranteePercent: v}
		require.NoError(t, in.Validate(), "cpu_guarantee_percent=%d must be valid", v)
	}
	for _, v := range []int32{-1, 101, 1000} {
		in := &Instance{CPUGuaranteePercent: v}
		require.Error(t, in.Validate(), "cpu_guarantee_percent=%d must be rejected", v)
	}
}
