// Package fgaintent — SEC-D: serialisation of FGA owner-tuple register/unregister
// intents written to the compute_fga_register_outbox table (transactional outbox,
// epic §3.1 Variant A).
//
// kacho-compute does NOT talk to OpenFGA directly any more (epic requirement #6,
// GitHub Issue N5). Instead, on every resource Create/Delete it records an
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

// Payload — the JSONB stored in one outbox row. OQ-SEC-D-2: a SET of tuples per
// row (the whole tuple-set of one resource is one RegisterResource transaction in
// IAM). Today compute resources carry exactly one project-hierarchy tuple, but
// the set form keeps the contract stable if a creator/parent-link tuple is added.
type Payload struct {
	Tuples []Tuple `json:"tuples"`
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
