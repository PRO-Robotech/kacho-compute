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
// Format: `<domain>.<resource>.<verb>` per IAM permission catalog (Phase 1).
const (
	ActionInstanceRead  = "compute.instances.read"
	ActionDiskRead      = "compute.disks.read"
	ActionImageRead     = "compute.images.read"
	ActionSnapshotRead  = "compute.snapshots.read"
	// ActionOperationRead — used to filter ListOperations result by op-id
	// when scope is per-resource.
	ActionOperationRead = "compute.operations.read"
)
