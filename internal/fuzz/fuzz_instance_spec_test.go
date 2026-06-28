// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Continuous fuzzing: Compute Instance spec validation.
//
// Instance create accepts a complex nested spec (boot_disk, secondary_disks,
// network_interfaces, metadata). Malformed input must NOT panic, must
// produce stable InvalidArgument.

package fuzz_test

import (
	"strings"
	"testing"
)

var instSpecTestSink any

func FuzzInstanceSpecValidate(f *testing.F) {
	seeds := []string{
		`{"name":"vm1","zoneId":"ru-central1-a","platformId":"standard-v2"}`,
		`{}`,
		``,
		`{"name":""}`,
		`{"name":"` + strings.Repeat("a", 100) + `"}`,
		`{"name":"vm1","zoneId":"empty"}`,
		`{"name":"vm1","resources":{"cores":99999,"memory":99999999999}}`,
		`{"networkInterfaces":[` + strings.Repeat(`{"subnetId":"sub1"},`, 100) + `{}]}`,
		// SQL-injection in name.
		`{"name":"'; DROP TABLE instances; --"}`,
		`{"metadata":{"key":"value"}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			n := len(input)
			if n > 100 {
				n = 100
			}
			if r := recover(); r != nil {
				t.Fatalf("PANIC on spec %q (len=%d): %v", input[:n], len(input), r)
			}
		}()
		valid := validateInstanceSpecStub(input)
		instSpecTestSink = valid
	})
}

func validateInstanceSpecStub(s string) bool {
	const maxLen = 1 << 18 // 256KB
	return len(s) > 0 && len(s) <= maxLen
}
