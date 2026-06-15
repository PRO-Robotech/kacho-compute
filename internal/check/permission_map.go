package check

import (
	"github.com/PRO-Robotech/kacho-corelib/authz"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

// FGA object types для kacho-compute (FGA model E3 §4 acceptance).
const (
	objectTypeProject  = "project"
	objectTypeDisk     = "compute_disk"
	objectTypeImage    = "compute_image"
	objectTypeSnapshot = "compute_snapshot"
	objectTypeInstance = "compute_instance"

	// DiskType / Region / Zone — глобальные read-only справочники. Доступ —
	// "viewer on project:<project_id>" недоступен (request не несёт project_id).
	// Решение: справочники видимы всем authenticated principal'ам на
	// cluster singleton (паттерн `viewer on cluster:cluster_kacho_root`).
	// KAC-178 §3: switched from "system:catalog" → "cluster:cluster_kacho_root"
	// — FGA model имеет `type cluster` с `viewer: [user, user:*] or ...`,
	// type `system` не определён → старая семантика всегда deny. Mirror
	// api-gateway permission catalog (compute.zones/regions Get/List scope
	// на cluster). cluster_kacho_root — singleton (kacho-iam ClusterSingletonID),
	// один на весь deploy.
	objectTypeCluster      = "cluster"
	clusterSingletonObject = "cluster_kacho_root"
)

const (
	relationViewer = "viewer"
	relationEditor = "editor"

	// relationSystemAdmin — cluster-scoped admin relation for kacho-only catalog
	// mutations (Internal{DiskType,Zone,Region}Service Create/Update/Delete).
	// Mirrors proto annotation `required_relation=system_admin`, object_type=cluster
	// in kacho-proto/.../internal_catalog_service.proto. Checked on the cluster
	// singleton (`system_admin on cluster:cluster_kacho_root`).
	relationSystemAdmin = "system_admin"
)

// staticClusterCatalog — extractor, всегда возвращающий (cluster, cluster_kacho_root).
// Используется для DiskType/Zone/Region read-only RPC. KAC-178 §3 — заменил
// staticClusterCatalog (`system:catalog`).
func staticClusterCatalog() authz.ObjectExtractor {
	return func(req any) (string, string, error) {
		return objectTypeCluster, clusterSingletonObject, nil
	}
}

// PermissionMap — карта RPC → required relation+extract.
//
// Семантика (см. kacho-vpc/internal/apps/kacho/check/permission_map.go):
//   - Create / List         — на parent scope `project:<project_id>`
//   - Get/Update/Delete/<verb> — на самом ресурсе `<resource_type>:<resource_id>`
//   - OperationService.Get  — viewer на `compute_operation:<id>`
//   - DiskType/Zone/Region Get/List — viewer на `system:catalog`
//   - SetAccessBindings/ListAccessBindings (kacho-iam-on-resource ACL) —
//     viewer/editor на самом ресурсе.
//
// scope-guard (KAC-108): для Update/Delete/<verb> мы НЕ резолвим project_id
// из БД заранее — проверка на самом ресурсе достаточно через FGA cascade
// (`editor on compute_instance` ← `editor on project`).
func PermissionMap() authz.RPCMap {
	return authz.RPCMap{
		// =========================
		// DiskService
		// =========================
		"/kacho.cloud.compute.v1.DiskService/Get": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.GetDiskRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/List": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.ListDisksRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/Create": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.CreateDiskRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/Update": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.UpdateDiskRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/Delete": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.DeleteDiskRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/Relocate": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.RelocateDiskRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/ListOperations": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.ListDiskOperationsRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/ListSnapshotSchedules": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.ListDiskSnapshotSchedulesRequest).GetDiskId(), nil
			}),
		},

		// =========================
		// ImageService
		// =========================
		"/kacho.cloud.compute.v1.ImageService/Get": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeImage, func(req any) (string, error) {
				return req.(*computev1.GetImageRequest).GetImageId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/GetLatestByFamily": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.GetImageLatestByFamilyRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/List": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.ListImagesRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/Create": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.CreateImageRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/Update": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeImage, func(req any) (string, error) {
				return req.(*computev1.UpdateImageRequest).GetImageId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/Delete": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeImage, func(req any) (string, error) {
				return req.(*computev1.DeleteImageRequest).GetImageId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/ListOperations": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeImage, func(req any) (string, error) {
				return req.(*computev1.ListImageOperationsRequest).GetImageId(), nil
			}),
		},

		// =========================
		// SnapshotService
		// =========================
		"/kacho.cloud.compute.v1.SnapshotService/Get": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeSnapshot, func(req any) (string, error) {
				return req.(*computev1.GetSnapshotRequest).GetSnapshotId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.SnapshotService/List": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.ListSnapshotsRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.SnapshotService/Create": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.CreateSnapshotRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.SnapshotService/Update": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeSnapshot, func(req any) (string, error) {
				return req.(*computev1.UpdateSnapshotRequest).GetSnapshotId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.SnapshotService/Delete": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeSnapshot, func(req any) (string, error) {
				return req.(*computev1.DeleteSnapshotRequest).GetSnapshotId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.SnapshotService/ListOperations": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeSnapshot, func(req any) (string, error) {
				return req.(*computev1.ListSnapshotOperationsRequest).GetSnapshotId(), nil
			}),
		},

		// =========================
		// InstanceService — lifecycle-heavy ресурс
		// =========================
		"/kacho.cloud.compute.v1.InstanceService/Get": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.GetInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/List": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.ListInstancesRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Create": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*computev1.CreateInstanceRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Update": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.UpdateInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/UpdateMetadata": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.UpdateInstanceMetadataRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Delete": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.DeleteInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/GetSerialPortOutput": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.GetInstanceSerialPortOutputRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Start": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.StartInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Stop": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.StopInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Restart": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.RestartInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/AttachDisk": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.AttachInstanceDiskRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/DetachDisk": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.DetachInstanceDiskRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/AttachFilesystem": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.AttachInstanceFilesystemRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/DetachFilesystem": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.DetachInstanceFilesystemRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/AddOneToOneNat": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.AddInstanceOneToOneNatRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/RemoveOneToOneNat": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.RemoveInstanceOneToOneNatRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/UpdateNetworkInterface": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.UpdateInstanceNetworkInterfaceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/ListOperations": {
			Relation: relationViewer,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.ListInstanceOperationsRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Relocate": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.RelocateInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/SimulateMaintenanceEvent": {
			Relation: relationEditor,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.SimulateInstanceMaintenanceEventRequest).GetInstanceId(), nil
			}),
		},

		// =========================
		// DiskType / Zone / Region — read-only catalog (viewer on system:catalog)
		// =========================
		"/kacho.cloud.compute.v1.DiskTypeService/Get": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.DiskTypeService/List": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.ZoneService/Get": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.ZoneService/List": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.RegionService/Get": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.RegionService/List": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},

		// =========================
		// Internal{DiskType,Zone,Region}Service — kacho-only catalog admin CRUD
		// (Create/Update/Delete) on the cluster-internal listener (:9091). KAC-31:
		// the internal listener now runs the same per-RPC FGA Check as public, so
		// these relation-gated RPCs MUST be mapped (else methodIsInternal-фолбэк
		// would silently bypass them). Each requires `system_admin on
		// cluster:cluster_kacho_root` — mirrors proto `required_relation=system_admin`,
		// object_type=cluster. InternalWatchService/Watch is proto `<exempt>` and is
		// intentionally NOT mapped (stays exempt via methodIsInternal).
		"/kacho.cloud.compute.v1.InternalDiskTypeService/Create": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalDiskTypeService/Update": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalDiskTypeService/Delete": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalZoneService/Create": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalZoneService/Update": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalZoneService/Delete": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalRegionService/Create": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalRegionService/Update": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.InternalRegionService/Delete": {
			Relation: relationSystemAdmin,
			Extract:  staticClusterCatalog(),
		},

		// =========================
		// OperationService (LRO; viewer на operation-id).
		// Proto-пакет — `kacho.cloud.operation` (без `.v1`); fullMethod
		// соответственно `/kacho.cloud.operation.OperationService/*`.
		// =========================
		// KAC-127: Operation poll is NOT gated per-RPC. The FGA model has no
		// `compute_operation` object type and no per-operation tuples are
		// emitted, so a `viewer on compute_operation:<id>` Check has no path
		// and every poll — including the creating client's own poll right
		// after a successful mutation — was denied. Operation ids are opaque
		// and unguessable; the api-gateway already marks these `<exempt>`.
		// Public here makes the compute interceptor consistent with the
		// gateway (a map-miss would fail-closed with ErrUnmapped).
		"/kacho.cloud.operation.OperationService/Get":    {Public: true},
		"/kacho.cloud.operation.OperationService/Cancel": {Public: true},
	}
}
