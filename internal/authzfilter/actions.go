package authzfilter

// Compute-domain FGA object types (consumed by iam.ListObjects.resource_type).
// Must stay in sync with internal/check/permission_map.go.
const (
	ResourceTypeInstance = "compute_instance"
	ResourceTypeDisk     = "compute_disk"
	ResourceTypeImage    = "compute_image"
	ResourceTypeSnapshot = "compute_snapshot"
	// ResourceTypeSystem — catalog (DiskType / Zone / Region); bypassed via
	// BypassFilter, but we keep the constant for explicit documentation.
	ResourceTypeSystem = "system"
)

// Compute-domain action strings — server-side resolves to FGA relation.
// Format: `<domain>.<resource>.<verb>`.
//
// D-consumer (§11, D-40..D-45 / issue #111): the verb MUST be one the
// kacho-iam AuthorizeService.ListObjects server maps to the FGA `viewer`
// relation. Under the scope_grant rules-model (sub-phase B/C/#193),
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
	// ActionOperationRead — used to filter ListOperations result by op-id
	// when scope is per-resource. Verb "list" → viewer (read==enforce).
	ActionOperationRead = "compute.operations.list"
)
