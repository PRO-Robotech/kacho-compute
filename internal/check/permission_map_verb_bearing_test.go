package check_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-compute/internal/check"
)

// Целевая (Design-B) карта enforcement-relation'ов для kacho-compute: per-RPC
// Check резолвит action на verb-bearing relation (`v_get`/`v_update`/`v_delete`/
// `v_list`), а не на tier. object-self RPC (Extract → id ресурса) флипается на v_*;
// parent-scoped Create (Extract → project) остаётся tier `editor`; top-level
// project-List остаётся `viewer` (visibility — через iam ListObjects union).

var verbGetRPCs = []string{
	"/kacho.cloud.compute.v1.DiskService/Get",
	"/kacho.cloud.compute.v1.ImageService/Get",
	"/kacho.cloud.compute.v1.SnapshotService/Get",
	"/kacho.cloud.compute.v1.InstanceService/Get",
	"/kacho.cloud.compute.v1.InstanceService/GetSerialPortOutput",
}

var verbUpdateRPCs = []string{
	"/kacho.cloud.compute.v1.DiskService/Update",
	"/kacho.cloud.compute.v1.DiskService/Relocate",
	"/kacho.cloud.compute.v1.ImageService/Update",
	"/kacho.cloud.compute.v1.SnapshotService/Update",
	"/kacho.cloud.compute.v1.InstanceService/Update",
	"/kacho.cloud.compute.v1.InstanceService/UpdateMetadata",
	"/kacho.cloud.compute.v1.InstanceService/Start",
	"/kacho.cloud.compute.v1.InstanceService/Stop",
	"/kacho.cloud.compute.v1.InstanceService/Restart",
	"/kacho.cloud.compute.v1.InstanceService/AttachDisk",
	"/kacho.cloud.compute.v1.InstanceService/DetachDisk",
	"/kacho.cloud.compute.v1.InstanceService/AttachFilesystem",
	"/kacho.cloud.compute.v1.InstanceService/DetachFilesystem",
	"/kacho.cloud.compute.v1.InstanceService/AddOneToOneNat",
	"/kacho.cloud.compute.v1.InstanceService/RemoveOneToOneNat",
	"/kacho.cloud.compute.v1.InstanceService/UpdateNetworkInterface",
	"/kacho.cloud.compute.v1.InstanceService/Relocate",
	"/kacho.cloud.compute.v1.InstanceService/SimulateMaintenanceEvent",
}

var verbDeleteRPCs = []string{
	"/kacho.cloud.compute.v1.DiskService/Delete",
	"/kacho.cloud.compute.v1.ImageService/Delete",
	"/kacho.cloud.compute.v1.SnapshotService/Delete",
	"/kacho.cloud.compute.v1.InstanceService/Delete",
}

var verbListOnResourceRPCs = []string{
	"/kacho.cloud.compute.v1.DiskService/ListOperations",
	"/kacho.cloud.compute.v1.DiskService/ListSnapshotSchedules",
	"/kacho.cloud.compute.v1.ImageService/ListOperations",
	"/kacho.cloud.compute.v1.SnapshotService/ListOperations",
	"/kacho.cloud.compute.v1.InstanceService/ListOperations",
}

var createChildRPCs = []string{
	"/kacho.cloud.compute.v1.DiskService/Create",
	"/kacho.cloud.compute.v1.ImageService/Create",
	"/kacho.cloud.compute.v1.SnapshotService/Create",
	"/kacho.cloud.compute.v1.InstanceService/Create",
}

var projectListRPCs = []string{
	"/kacho.cloud.compute.v1.DiskService/List",
	"/kacho.cloud.compute.v1.ImageService/List",
	"/kacho.cloud.compute.v1.SnapshotService/List",
	"/kacho.cloud.compute.v1.InstanceService/List",
}

func TestPermissionMap_VerbBearing_Get_VGet(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range verbGetRPCs {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "v_get", e.Relation, "%s: object-self read must enforce v_get (Design B)", rpc)
	}
}

func TestPermissionMap_VerbBearing_Update_VUpdate(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range verbUpdateRPCs {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "v_update", e.Relation, "%s: object-self mutation must enforce v_update (Design B)", rpc)
	}
}

func TestPermissionMap_VerbBearing_Delete_VDelete(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range verbDeleteRPCs {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "v_delete", e.Relation, "%s: object-self delete must enforce v_delete (Design B)", rpc)
	}
}

func TestPermissionMap_VerbBearing_ListOnResource_VList(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range verbListOnResourceRPCs {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "v_list", e.Relation, "%s: object-self list-on-resource must enforce v_list (Design B)", rpc)
	}
}

func TestPermissionMap_VerbBearing_CreateChild_StaysEditor(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range createChildRPCs {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "editor", e.Relation, "%s: create-child stays tier editor on parent project (F-7)", rpc)
	}
}

func TestPermissionMap_VerbBearing_ProjectList_StaysViewer(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range projectListRPCs {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "viewer", e.Relation, "%s: top-level project List stays viewer (visibility via iam ListObjects union)", rpc)
	}
}

// TestPermissionMap_VerbBearing_DiskTypeCatalogUnchanged — DiskType read-only
// catalog гейтится `viewer` на cluster singleton (cluster — не verb-bearing, F-8);
// не флипается.
func TestPermissionMap_VerbBearing_DiskTypeCatalogUnchanged(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range []string{
		"/kacho.cloud.compute.v1.DiskTypeService/Get",
		"/kacho.cloud.compute.v1.DiskTypeService/List",
	} {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "viewer", e.Relation, "%s: cluster-catalog read stays viewer (F-8)", rpc)
	}
}

// TestPermissionMap_VerbBearing_InternalUnchanged — internal catalog-admin RPC
// остаются system_admin@cluster (cluster — не verb-bearing, F-8).
func TestPermissionMap_VerbBearing_InternalUnchanged(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range []string{
		"/kacho.cloud.compute.v1.InternalDiskTypeService/Create",
		"/kacho.cloud.compute.v1.InternalDiskTypeService/Update",
		"/kacho.cloud.compute.v1.InternalDiskTypeService/Delete",
	} {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.Equalf(t, "system_admin", e.Relation, "%s: internal catalog-admin relation unchanged (F-8)", rpc)
	}
}

func TestPermissionMap_VerbBearing_NoTierLeftOnObjectSelf(t *testing.T) {
	m := check.PermissionMap()
	objectSelf := append(append(append(append([]string{}, verbGetRPCs...), verbUpdateRPCs...), verbDeleteRPCs...), verbListOnResourceRPCs...)
	for _, rpc := range objectSelf {
		e, ok := m[rpc]
		require.Truef(t, ok, "%s must be mapped", rpc)
		require.NotEqualf(t, "viewer", e.Relation, "%s: object-self must not stay on tier viewer", rpc)
		require.NotEqualf(t, "editor", e.Relation, "%s: object-self must not stay on tier editor", rpc)
	}
}
