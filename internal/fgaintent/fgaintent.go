// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package fgaintent — serialisation of FGA owner-tuple register/unregister intents
// written to the compute_fga_register_outbox table (transactional outbox).
//
// kacho-compute does NOT talk to OpenFGA directly any more. Instead, on every
// resource Create/Delete it records an
// "intent" row IN THE SAME writer-tx as the resource Insert/Delete. A separate
// register-drainer (corelib outbox/drainer) later replays each intent by calling
// kacho-iam InternalIAMService.RegisterResource / UnregisterResource over mTLS —
// idempotent, at-least-once, IAM-Unavailable → retry, the owner-tuple is never
// lost (the dual-write bug N5 is gone: no best-effort post-commit FGA write).
//
// This package is a leaf (stdlib only): it is imported by the repo writer-side
// (emit) and by the clients applier-side (decode) without an import cycle and
// without dragging pgx/grpc into either edge of the contract.
package fgaintent

import (
	"encoding/json"
	"fmt"
	"time"
)

// Event types stored in compute_fga_register_outbox.event_type (matches the
// migration CHECK constraint). On a resource Create → EventRegister; on Delete →
// EventUnregister.
const (
	EventRegister   = "fga.register"
	EventUnregister = "fga.unregister"
)

// Tuple — a single FGA owner-hierarchy tuple intent: subject `relation`-on-object.
// Mirrors the kacho-proto RegisterResourceRequest triple
// (subject_id / relation / object). Wire form is FGA authorization-model strings
// (NOT the permission-catalog), e.g.
//
//	{SubjectID: "project:prj-xxx", Relation: "project", Object: "compute_instance:epd-xxx"}
type Tuple struct {
	SubjectID string `json:"subject_id"`
	Relation  string `json:"relation"`
	Object    string `json:"object"`
}

// Payload — the JSONB stored in one outbox row. A SET of tuples per row (the
// whole tuple-set of one resource is one RegisterResource transaction in
// IAM). Today compute resources carry exactly one project-hierarchy tuple, but
// the set form keeps the contract stable if a creator/parent-link tuple is added.
//
// Epic Resource-scoped-AccessBinding β: the payload also carries the owner's
// labels + parent-scope (project / account). The register-drainer forwards them
// to IAM.RegisterResource so kacho-iam can populate its output-only
// resource_mirror (label+parent mirror that feeds the γ selector / containment
// gate, SAME-DB in IAM, without an iam→compute edge — data is pushed by the
// consumer, IAM never pulls). These fields are additive and optional — older
// payloads decode with empty values (back-compat).
type Payload struct {
	Tuples []Tuple `json:"tuples"`
	// Labels — copy of the owner resource's labels (β mirror; for the γ selector).
	Labels map[string]string `json:"labels,omitempty"`
	// ParentProjectID — the owning project id (β parent-scope; for γ containment).
	ParentProjectID string `json:"parent_project_id,omitempty"`
	// ParentAccountID — the owning account id, when the producer can resolve it
	// (β parent-scope). compute leaves it empty today (no project→account resolve
	// on the resource hot-path); IAM handles an empty parent gracefully.
	ParentAccountID string `json:"parent_account_id,omitempty"`
	// SourceVersion — monotonic per-object marker (epic RSAB β-hardening). Stamped
	// from the DB clock (now()) at the moment THIS intent row is INSERTed, inside
	// the SAME writer-tx as the resource mutation. For sequential mutations of one
	// object a later mutation's tx commits-after the earlier, so its now() is
	// strictly greater → monotonic per-object. The register-drainer forwards it as
	// RegisterResourceRequest.source_version so kacho-iam applies the mirror UPSERT
	// last-SOURCE-state-wins (a reordered stale intent → no-op, not an overwrite).
	// Compute has no per-row updated_at column, and the intent-emit now() is the
	// exact instant the source-state is recorded — a correct, least-invasive marker.
	// Zero (legacy payload / decode of an old row) → IAM treats as '-infinity'.
	SourceVersion time.Time `json:"source_version,omitempty"`
}

// fgaTypeByKind maps a compute outbox resource_kind ("Instance"/"Disk"/"Image"/
// "Snapshot") to its FGA authorization-model object type. The cascade
// `<rel> from project` on every compute_* type is what lets a per-resource Check
// resolve through the project where the principal's role binding lives — the same
// mapping the deleted openfga_write_client.go used.
var fgaTypeByKind = map[string]string{
	"Instance": "compute_instance",
	"Disk":     "compute_disk",
	"Image":    "compute_image",
	"Snapshot": "compute_snapshot",
}

// FGAType returns the compute_* FGA object type for a resource_kind, or "" if the
// kind is unknown (caller skips intent emission for unknown kinds — fail-safe).
func FGAType(kind string) string { return fgaTypeByKind[kind] }

// ProjectHierarchyTuple builds the project-hierarchy owner-tuple for a freshly
// created (or to-be-deleted) compute resource:
//
//	project:<projectID> #project @compute_<kind>:<resourceID>
//
// Returns ok=false when kind is unknown or projectID/resourceID is empty (caller
// must not emit an empty / malformed intent).
func ProjectHierarchyTuple(kind, resourceID, projectID string) (Tuple, bool) {
	objType := FGAType(kind)
	if objType == "" || resourceID == "" || projectID == "" {
		return Tuple{}, false
	}
	return Tuple{
		SubjectID: "project:" + projectID,
		Relation:  "project",
		Object:    fmt.Sprintf("%s:%s", objType, resourceID),
	}, true
}

// Encode marshals a Payload to JSONB bytes for the outbox row.
func Encode(p Payload) ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("fgaintent: marshal payload: %w", err)
	}
	return b, nil
}

// Decode unmarshals an outbox-row JSONB payload back into a Payload.
func Decode(b []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		return Payload{}, fmt.Errorf("fgaintent: unmarshal payload: %w", err)
	}
	return p, nil
}
