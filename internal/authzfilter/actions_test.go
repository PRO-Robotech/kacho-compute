// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// actions_test.go — the compute list-filter must call iam
// AuthorizeService.ListObjects with an `action` whose verb the iam server
// resolves to the FGA `viewer` relation (read==enforce parity under the
// scope_grant rules-model).
//
// Why this test exists (the bug it pins):
//
//	Before this fix, compute sent action="compute.<resource>.read" (verb
//	"read"). The iam ListObjects server (kacho-iam internal/service/
//	authorize_service.go::resolveActionToRelation) maps ONLY the canonical RPC
//	verbs get/list → "viewer"; the verb "read" is NOT in the map → it returns
//	"" → ListObjects answers `Illegal argument action` (InvalidArgument). The
//	compute filter then wraps that as Unavailable for every List → with
//	list-filter.enabled=true ALL public Lists break (fail-closed on a contract
//	mismatch, not on a real authz denial).
//
//	D-consumer requires read==enforce: List visibility must use the SAME
//	relation the per-RPC Check gate uses for Get/List, which is "viewer"
//	(internal/check/permission_map.go). Under the scope_grant model "viewer"
//	cascades from an account-tier scope_grant (g_viewer_compute_<type>), so a
//	rules-role list grant becomes visible per-object via ListObjects(viewer).
//
// This test embeds a copy of the iam verb→relation contract as the single
// source of truth and asserts each compute List action resolves to "viewer".
// It is RED while the constants carry the ".read" verb and GREEN once they
// carry ".list".
package authzfilter

import "testing"

// resolveActionToRelationIAM mirrors the CONTRACT enforced by kacho-iam
// internal/service/authorize_service.go::resolveActionToRelation for the verbs
// relevant to read-path ListObjects. This is intentionally a small, faithful
// copy of the cross-repo contract (compute cannot import iam internals): the
// iam server splits "<domain>.<resource>.<verb>", lower-cases the verb, and
// maps get/list → "viewer". An unmapped verb (e.g. "read") → "" → the iam
// server answers InvalidArgument.
//
// If the iam contract changes, this copy and the action constants must be
// updated together (and the newman D-cases catch drift end-to-end).
func resolveActionToRelationIAM(action string) string {
	// last dot-segment is the verb.
	last := -1
	for i := 0; i < len(action); i++ {
		if action[i] == '.' {
			last = i
		}
	}
	if last < 0 || last == len(action)-1 {
		return ""
	}
	verb := action[last+1:]
	switch verb {
	case "get", "list":
		return "viewer"
	}
	// All other verbs (create/update/delete/<domain-action>) are irrelevant for
	// the read-path list-filter; and notably "read" is NOT mapped → "".
	return ""
}

// TestListActions_ResolveToViewer — every compute public-List action MUST
// resolve to the "viewer" relation on the iam ListObjects server (read==enforce,
// D-40..D-45). RED on ".read"; GREEN on ".list".
func TestListActions_ResolveToViewer(t *testing.T) {
	cases := []struct {
		name   string
		action string
	}{
		{"instance", ActionInstanceRead},
		{"disk", ActionDiskRead},
		{"image", ActionImageRead},
		{"snapshot", ActionSnapshotRead},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveActionToRelationIAM(c.action)
			if got != "viewer" {
				t.Fatalf("action %q must resolve to FGA relation %q on the iam ListObjects "+
					"server (read==enforce, D-40..D-45), got %q. The verb must be one the "+
					"iam resolveActionToRelation maps to viewer (get/list), NOT \"read\".",
					c.action, "viewer", got)
			}
		})
	}
}

// TestListActions_NotReadVerb — guards against regressing to the ".read" verb,
// which the iam ListObjects server rejects with InvalidArgument (the exact
// D-consumer bug). The list-filter read-path action verb must be "list".
func TestListActions_VerbIsList(t *testing.T) {
	for _, action := range []string{ActionInstanceRead, ActionDiskRead, ActionImageRead, ActionSnapshotRead} {
		last := -1
		for i := 0; i < len(action); i++ {
			if action[i] == '.' {
				last = i
			}
		}
		verb := action[last+1:]
		if verb != "list" {
			t.Fatalf("action %q: list-filter verb must be %q (iam maps it to viewer); got %q. "+
				"The verb \"read\" is unmapped by iam → ListObjects returns InvalidArgument → "+
				"every compute List breaks fail-closed.", action, "list", verb)
		}
	}
}
