// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Continuous fuzzing: Compute Instance create-spec validation.
//
// Instance create accepts a complex nested spec (boot_disk, secondary_disks,
// network_interfaces, metadata). This target drives the SAME production path the
// RPC runs on hostile input:
//
//	protojson → computev1.CreateInstanceRequest → handler.CreateReqFromProto →
//	service.ValidateCreateInstanceReq
//
// Invariants asserted:
//   - malformed input must NOT panic anywhere on that path (proto decode,
//     conversion, synchronous field validation);
//   - a validation rejection must be a stable gRPC InvalidArgument (never a bare
//     error / other code), so the RPC surface never leaks an internal fault as a
//     client-caused one.
package fuzz_test

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/handler"
	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

var instSpecTestSink error

func FuzzInstanceSpecValidate(f *testing.F) {
	seeds := []string{
		`{"name":"vm1","zoneId":"ru-central1-a","platformId":"standard-v2"}`,
		`{}`,
		``,
		`{"name":""}`,
		`{"name":"` + strings.Repeat("a", 100) + `"}`,
		`{"name":"vm1","zoneId":"empty"}`,
		`{"name":"vm1","resourcesSpec":{"cores":99999,"memory":99999999999,"coreFraction":37}}`,
		`{"secondaryDiskSpecs":[` + strings.Repeat(`{"diskId":"epd1"},`, 100) + `{}]}`,
		// SQL-injection in name.
		`{"name":"'; DROP TABLE instances; --"}`,
		`{"metadata":{"key":"value"}}`,
		// well-formed happy spec — exercises the accept path (err == nil).
		`{"projectId":"prj1","name":"vm1","zoneId":"ru-central1-a","platformId":"standard-v2",` +
			`"resourcesSpec":{"cores":2,"memory":2147483648},"bootDiskSpec":{"diskId":"epd1"}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Real wire decode: malformed JSON is an expected outcome (skip), a PANIC
		// is the bug we hunt. Discard decode errors rather than fail — the fuzzer
		// explores the decoder itself for crashes.
		req := &computev1.CreateInstanceRequest{}
		if err := protojson.Unmarshal([]byte(input), req); err != nil {
			return
		}

		// Same conversion + synchronous validation the RPC runs.
		cr := handler.CreateReqFromProto(req)
		err := service.ValidateCreateInstanceReq(cr)
		instSpecTestSink = err
		if err == nil {
			return
		}
		// A rejection must be a stable InvalidArgument — not Unknown/Internal or a
		// non-status error (which would surface to clients as INTERNAL).
		if code := status.Code(err); code != codes.InvalidArgument {
			t.Fatalf("validation rejection must be InvalidArgument, got %s: %v (input=%q)", code, err, input)
		}
	})
}
