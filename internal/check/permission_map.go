// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package check

import (
	"github.com/PRO-Robotech/kacho-corelib/authz"
	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

// FGA object types для kacho-compute (соответствуют FGA-модели kacho-iam).
const (
	objectTypeProject  = "project"
	objectTypeDisk     = "compute_disk"
	objectTypeImage    = "compute_image"
	objectTypeSnapshot = "compute_snapshot"
	objectTypeInstance = "compute_instance"

	// DiskType — глобальный read-only справочник. Доступ —
	// "viewer on project:<project_id>" недоступен (request не несёт project_id).
	// Решение: справочник видим всем authenticated principal'ам на
	// cluster singleton (паттерн `viewer on cluster:cluster_kacho_root`).
	// Catalog object — "cluster:cluster_kacho_root": FGA model имеет `type cluster`
	// с `viewer: [user, user:*] or ...`. cluster_kacho_root — singleton (kacho-iam
	// ClusterSingletonID), один на весь deploy. Region/Zone serving снят — Geography
	// принадлежит kacho-geo.
	objectTypeCluster      = "cluster"
	clusterSingletonObject = "cluster_kacho_root"
)

const (
	// relationViewer / relationEditor — tier-relations. Сохраняются для
	// create-child (на parent project, F-7), top-level project-List (visibility
	// per-object идёт через iam ListObjects `viewer ∪ v_list`, не через per-RPC
	// Check) и DiskType read-only catalog (cluster — не verb-bearing). Для
	// object-self CRUD энфорс — verb-bearing relations ниже.
	relationViewer = "viewer"
	relationEditor = "editor"

	// verb-bearing relations (v_*) — enforcement резолвит object-self action на
	// verb, а не на tier (anchor-эпик «Explicit RBAC model 2026», D-6/D-6a:
	// доступ по verb развязан с tier). Материализуются per-object reconciler'ом
	// kacho-iam; consumer гейтит ими object-self RPC. Source of truth relation-имён
	// — kacho-iam/internal/authzmap; тут — backend view-only.
	//
	//	v_get    — чтение содержимого самого ресурса (Get / GetSerialPortOutput);
	//	v_list   — видимость операций/дочерних на самом ресурсе (ListOperations,
	//	           ListSnapshotSchedules) — НЕ top-level project-List;
	//	v_update — мутация самого ресурса (Update + lifecycle/attach/detach verb'ы);
	//	v_delete — удаление самого ресурса.
	relationVGet    = "v_get"
	relationVList   = "v_list"
	relationVUpdate = "v_update"
	relationVDelete = "v_delete"

	// relationSystemAdmin — cluster-scoped admin relation for kacho-only catalog
	// mutations (InternalDiskTypeService Create/Update/Delete).
	// Mirrors proto annotation `required_relation=system_admin`, object_type=cluster
	// in kacho-proto/.../internal_catalog_service.proto. Checked on the cluster
	// singleton (`system_admin on cluster:cluster_kacho_root`).
	relationSystemAdmin = "system_admin"
)

// staticClusterCatalog — extractor, всегда возвращающий (cluster, cluster_kacho_root).
// Используется для DiskType read-only RPC.
func staticClusterCatalog() authz.ObjectExtractor {
	return func(req any) (string, string, error) {
		return objectTypeCluster, clusterSingletonObject, nil
	}
}

// PermissionMap — карта RPC → required relation+extract.
//
// Семантика per-RPC (enforcement по verb, развязан с tier):
//   - Create                          — parent scope `project:<project_id>`, tier `editor` (F-7)
//   - top-level List                  — parent scope `project:<project_id>`, tier `viewer`;
//     visibility per-object — через iam ListObjects `viewer ∪ v_list`
//   - Get / GetSerialPortOutput       — на самом ресурсе, `v_get`
//   - ListOperations/Snapshot… (on res) — на самом ресурсе, `v_list`
//   - Update + lifecycle/attach verb'ы — на самом ресурсе, `v_update`
//   - Delete                          — на самом ресурсе, `v_delete`
//   - DiskType Get/List               — `viewer` на cluster singleton (не verb-bearing)
//   - OperationService.Get            — Public (op-id opaque, поллится creator'ом)
//
// scope-guard: для object-self RPC мы НЕ резолвим project_id из БД
// заранее — проверяем v_*-relation на самом ресурсе. reconciler kacho-iam
// материализует per-object `v_get/v_list/v_update/v_delete` для grant'а
// соответствующего verb'а; cluster-admin резолвится через iam short-circuit.
func PermissionMap() authz.RPCMap {
	return authz.RPCMap{
		// =========================
		// DiskService
		// =========================
		"/kacho.cloud.compute.v1.DiskService/Get": {
			Relation: relationVGet,
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
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.UpdateDiskRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/Delete": {
			Relation: relationVDelete,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.DeleteDiskRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/Relocate": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.RelocateDiskRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/ListOperations": {
			Relation: relationVList,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.ListDiskOperationsRequest).GetDiskId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.DiskService/ListSnapshotSchedules": {
			Relation: relationVList,
			Extract: authz.StaticExtractor(objectTypeDisk, func(req any) (string, error) {
				return req.(*computev1.ListDiskSnapshotSchedulesRequest).GetDiskId(), nil
			}),
		},

		// =========================
		// ImageService
		// =========================
		"/kacho.cloud.compute.v1.ImageService/Get": {
			Relation: relationVGet,
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
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeImage, func(req any) (string, error) {
				return req.(*computev1.UpdateImageRequest).GetImageId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/Delete": {
			Relation: relationVDelete,
			Extract: authz.StaticExtractor(objectTypeImage, func(req any) (string, error) {
				return req.(*computev1.DeleteImageRequest).GetImageId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.ImageService/ListOperations": {
			Relation: relationVList,
			Extract: authz.StaticExtractor(objectTypeImage, func(req any) (string, error) {
				return req.(*computev1.ListImageOperationsRequest).GetImageId(), nil
			}),
		},

		// =========================
		// SnapshotService
		// =========================
		"/kacho.cloud.compute.v1.SnapshotService/Get": {
			Relation: relationVGet,
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
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeSnapshot, func(req any) (string, error) {
				return req.(*computev1.UpdateSnapshotRequest).GetSnapshotId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.SnapshotService/Delete": {
			Relation: relationVDelete,
			Extract: authz.StaticExtractor(objectTypeSnapshot, func(req any) (string, error) {
				return req.(*computev1.DeleteSnapshotRequest).GetSnapshotId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.SnapshotService/ListOperations": {
			Relation: relationVList,
			Extract: authz.StaticExtractor(objectTypeSnapshot, func(req any) (string, error) {
				return req.(*computev1.ListSnapshotOperationsRequest).GetSnapshotId(), nil
			}),
		},

		// =========================
		// InstanceService — lifecycle-heavy ресурс
		// =========================
		"/kacho.cloud.compute.v1.InstanceService/Get": {
			Relation: relationVGet,
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
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.UpdateInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/UpdateMetadata": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.UpdateInstanceMetadataRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Delete": {
			Relation: relationVDelete,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.DeleteInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/GetSerialPortOutput": {
			Relation: relationVGet,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.GetInstanceSerialPortOutputRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Start": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.StartInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Stop": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.StopInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Restart": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.RestartInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/AttachDisk": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.AttachInstanceDiskRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/DetachDisk": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.DetachInstanceDiskRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/AttachFilesystem": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.AttachInstanceFilesystemRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/DetachFilesystem": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.DetachInstanceFilesystemRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/AddOneToOneNat": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.AddInstanceOneToOneNatRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/RemoveOneToOneNat": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.RemoveInstanceOneToOneNatRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/UpdateNetworkInterface": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.UpdateInstanceNetworkInterfaceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/ListOperations": {
			Relation: relationVList,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.ListInstanceOperationsRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/Relocate": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.RelocateInstanceRequest).GetInstanceId(), nil
			}),
		},
		"/kacho.cloud.compute.v1.InstanceService/SimulateMaintenanceEvent": {
			Relation: relationVUpdate,
			Extract: authz.StaticExtractor(objectTypeInstance, func(req any) (string, error) {
				return req.(*computev1.SimulateInstanceMaintenanceEventRequest).GetInstanceId(), nil
			}),
		},

		// =========================
		// DiskType — read-only catalog (viewer on cluster singleton). Region/Zone
		// serving removed — Geography is owned by kacho-geo.
		// =========================
		"/kacho.cloud.compute.v1.DiskTypeService/Get": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},
		"/kacho.cloud.compute.v1.DiskTypeService/List": {
			Relation: relationViewer,
			Extract:  staticClusterCatalog(),
		},

		// =========================
		// InternalDiskTypeService — kacho-only catalog admin CRUD
		// (Create/Update/Delete) on the cluster-internal listener (:9091). The
		// internal listener runs the same per-RPC FGA Check as public and the
		// pinned corelib authz.Interceptor has NO name-based "methodIsInternal"
		// fallback — every RPC absent from this map fails closed with
		// PermissionDenied ("rpc not mapped"), unary AND stream alike. So every
		// internal RPC MUST have an explicit entry: relation-gated (like the
		// three below, `system_admin on cluster:cluster_kacho_root` — mirrors
		// proto `required_relation=system_admin`, object_type=cluster) or
		// Public=true for an explicit exempt (like InternalWatchService/Watch
		// further down, and OperationService.Get/Cancel above).
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

		// InternalWatchService/Watch — internal server-stream over compute_outbox
		// (LISTEN/NOTIFY; see internal/handler/internal_watch_handler.go), registered
		// on the same internal :9091 listener/authzIntr.Stream() chain as the
		// catalog-admin RPCs above (cmd/compute/main.go). It carries no proto
		// required_relation and there is no natural per-object FGA target for a
		// cursor-based outbox tail, so it is explicitly exempt via Public=true —
		// the same documented mechanism as OperationService.Get/Cancel below, NOT
		// a name-based "methodIsInternal" skip (the pinned corelib has none: an
		// unmapped stream RPC fails closed with PermissionDenied).
		"/kacho.cloud.compute.v1.InternalWatchService/Watch": {Public: true},

		// =========================
		// OperationService (LRO; viewer на operation-id).
		// Proto-пакет — `kacho.cloud.operation` (без `.v1`); fullMethod
		// соответственно `/kacho.cloud.operation.OperationService/*`.
		// =========================
		// Operation poll is NOT gated by the FGA per-RPC Check: the FGA model has
		// no `compute_operation` object type and no per-operation tuples are
		// emitted, so a `viewer on compute_operation:<id>` Check has no path and
		// every poll — including the creating client's own poll right after a
		// successful mutation — would be denied. The api-gateway already marks
		// these `<exempt>`; Public here keeps the compute interceptor consistent
		// (a map-miss would fail-closed with ErrUnmapped).
		//
		// Access is instead bound to the operation's creating principal INSIDE the
		// OperationHandler (GetOwned/CancelOwned, ownership-predicate in SQL WHERE)
		// — a non-owner gets NotFound, no BOLA on the opaque id. See
		// internal/handler/operation_handler.go.
		"/kacho.cloud.operation.OperationService/Get":    {Public: true},
		"/kacho.cloud.operation.OperationService/Cancel": {Public: true},
	}
}
