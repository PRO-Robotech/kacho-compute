// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package fgaintent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
)

// TestProjectHierarchyTuple_Shape — the project-hierarchy owner-tuple is
// `project:<projectID> #project @compute_<kind>:<resourceID>` for every compute
// resource kind.
func TestProjectHierarchyTuple_Shape(t *testing.T) {
	cases := []struct {
		kind   string
		object string
	}{
		{"Instance", "compute_instance:epd-1"},
		{"Disk", "compute_disk:epd-2"},
		{"Image", "compute_image:fd8-3"},
		{"Snapshot", "compute_snapshot:fd8-4"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			id := c.object[len(c.object)-5:]
			tpl, ok := fgaintent.ProjectHierarchyTuple(c.kind, id, "prj-x")
			require.True(t, ok)
			assert.Equal(t, "project:prj-x", tpl.SubjectID)
			assert.Equal(t, "project", tpl.Relation)
			assert.Equal(t, c.object, tpl.Object)
		})
	}
}

// TestProjectHierarchyTuple_FailSafe — unknown kind / empty id / empty project →
// ok=false (caller must not emit an empty/malformed intent; the resource still
// commits, an unmappable kind simply has no FGA hierarchy to register).
func TestProjectHierarchyTuple_FailSafe(t *testing.T) {
	_, ok := fgaintent.ProjectHierarchyTuple("Bogus", "x", "prj")
	assert.False(t, ok, "unknown kind → no tuple")
	_, ok = fgaintent.ProjectHierarchyTuple("Instance", "", "prj")
	assert.False(t, ok, "empty resource id → no tuple")
	_, ok = fgaintent.ProjectHierarchyTuple("Instance", "epd-1", "")
	assert.False(t, ok, "empty project id → no tuple")
}

// TestEncodeDecode_Roundtrip — payload survives the outbox JSONB round-trip
// (writer encodes in the writer-tx, drainer decodes on apply).
func TestEncodeDecode_Roundtrip(t *testing.T) {
	in := fgaintent.Payload{Tuples: []fgaintent.Tuple{
		{SubjectID: "project:prj-x", Relation: "project", Object: "compute_instance:epd-1"},
	}}
	b, err := fgaintent.Encode(in)
	require.NoError(t, err)
	out, err := fgaintent.Decode(b)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

// Test_Beta01_PayloadCarriesLabelsAndParent — β-01: the register intent payload
// carries the owner's labels + parent-scope (project/account) so the register-
// drainer can forward them to IAM.RegisterResource (label+parent mirror sync).
// The mirror fields survive the JSONB round-trip alongside the owner-tuple set.
func Test_Beta01_PayloadCarriesLabelsAndParent(t *testing.T) {
	in := fgaintent.Payload{
		Tuples: []fgaintent.Tuple{
			{SubjectID: "project:prj-P", Relation: "project", Object: "compute_instance:epd-1"},
		},
		Labels:          map[string]string{"env": "dev", "team": "core"},
		ParentProjectID: "prj-P",
		ParentAccountID: "acc-A",
	}
	b, err := fgaintent.Encode(in)
	require.NoError(t, err)
	out, err := fgaintent.Decode(b)
	require.NoError(t, err)
	assert.Equal(t, in, out)
	assert.Equal(t, map[string]string{"env": "dev", "team": "core"}, out.Labels)
	assert.Equal(t, "prj-P", out.ParentProjectID)
	assert.Equal(t, "acc-A", out.ParentAccountID)
}

// TestDecode_Garbage — malformed JSON → error (drainer treats decode error as a
// permanent poison via errors.Join(ErrPermanent, …) at the call-site).
func TestDecode_Garbage(t *testing.T) {
	_, err := fgaintent.Decode([]byte("{not json"))
	require.Error(t, err)
}
