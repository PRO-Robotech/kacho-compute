// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

// Compute-domain FGA object types (consumed by iam.ListObjects.resource_type).
// Must stay in sync with internal/check/permission_map.go.
const (
	ResourceTypeInstance = "compute_instance"
	ResourceTypeDisk     = "compute_disk"
	ResourceTypeImage    = "compute_image"
	ResourceTypeSnapshot = "compute_snapshot"
)

// Compute-domain action strings — server-side resolves to FGA relation.
// Format: `<domain>.<resource>.<verb>`.
//
// The verb MUST be one the kacho-iam AuthorizeService.ListObjects server maps to
// the FGA `viewer` relation. Under the scope_grant rules-model,
// `resolveActionToRelation` maps ONLY the canonical RPC verbs get/list →
// "viewer"; the verb "read" is UNMAPPED → it returns "Illegal argument action"
// (InvalidArgument), which the compute filter wraps as Unavailable for every
// List — so list-filter.enabled=true would break ALL public Lists.
//
// "viewer" is also the SAME relation the per-RPC Check gate uses for Get/List
// (internal/check/permission_map.go) — so List visibility == Check-allow
// (read==enforce), and "viewer" cascades from an account-tier scope_grant
// (g_viewer_compute_<type>) so a rules-role list-grant becomes visible
// per-object via ListObjects(viewer).
const (
	ActionInstanceRead = "compute.instances.list"
	ActionDiskRead     = "compute.disks.list"
	ActionImageRead    = "compute.images.list"
	ActionSnapshotRead = "compute.snapshots.list"
)
