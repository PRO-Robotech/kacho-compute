// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package protoconv_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	computev1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/protoconv"
)

// TestInstance_OmitsInfraSensitivePlacement — регрессионный security-гейт вокруг
// единственной tenant-facing serialization-границы (protoconv.Instance).
//
// security.md: placement/host-инвентарь (host_id / host_group_id) — infra-sensitive,
// НИКОГДА не должен появляться на публичной gRPC/REST-поверхности (defense-in-depth
// против lateral-movement reconnaissance). Проекция host_id/host_group_id ранее была
// снята с публичного Instance-сообщения (proto-контракт больше их не объявляет);
// domain-поля и DB-колонки остались internal-only. Этот тест — недостающий guard: он
// фиксирует, что даже при заполненных domain.Instance.HostID/HostGroupID
// сериализованное публичное сообщение не содержит эти значения НИГДЕ (ни как
// отдельное поле, ни случайно затолканные в name/labels/metadata). Если будущая
// правка вернёт проекцию host-полей — тест краснеет.
func TestInstance_OmitsInfraSensitivePlacement(t *testing.T) {
	const (
		hostIDSentinel      = "HOSTID-INFRA-SENTINEL-ac9f"
		hostGroupIDSentinel = "HOSTGROUP-INFRA-SENTINEL-b7d2"
	)
	in := &domain.Instance{
		ID:          "epd0000000000000000",
		ProjectID:   "prj0000000000000000",
		Name:        "vm-1",
		Description: "public projection guard",
		Labels:      map[string]string{"env": "prod"},
		ZoneID:      "ru-central1-a",
		PlatformID:  "standard-v3",
		Status:      domain.InstanceStatusRunning,
		Metadata:    map[string]string{"ssh-keys": "user:key"},
		// infra-sensitive placement identifiers — kept in domain/DB, MUST NOT project.
		HostID:      hostIDSentinel,
		HostGroupID: hostGroupIDSentinel,
	}

	out := protoconv.Instance(in)
	require.NotNil(t, out)

	// Serialize the full public message and assert the placement sentinels appear
	// nowhere in it — robust against any leak path (dedicated field or a value
	// accidentally routed into another projected field).
	raw, err := protojson.Marshal(out)
	require.NoError(t, err)
	serialized := string(raw)

	assert.NotContains(t, serialized, hostIDSentinel,
		"host_id (infra-sensitive placement) leaked onto public Instance message")
	assert.NotContains(t, serialized, hostGroupIDSentinel,
		"host_group_id (infra-sensitive placement) leaked onto public Instance message")
}

// TestInstance_ProjectsExpectedFields — locks the tenant-facing projection: the
// fields that ARE part of the public contract must round-trip domain→proto. A
// regression that silently DROPS a legitimate field (e.g. zone_id, status) is
// caught here, complementing the infra-omission guard above.
func TestInstance_ProjectsExpectedFields(t *testing.T) {
	created := time.Date(2026, 7, 6, 10, 30, 45, 500_000_000, time.UTC)
	in := &domain.Instance{
		ID:                     "epd0000000000000001",
		ProjectID:              "prj0000000000000001",
		CreatedAt:              created,
		Name:                   "vm-full",
		Description:            "desc",
		Labels:                 map[string]string{"team": "core"},
		ZoneID:                 "ru-central1-b",
		PlatformID:             "standard-v3",
		Cores:                  4,
		Memory:                 8 << 30,
		CoreFraction:           100,
		GPUs:                   1,
		Status:                 domain.InstanceStatusStopped,
		Metadata:               map[string]string{"k": "v"},
		ServiceAccountID:       "sa0000000000000001",
		FQDN:                   "vm-full.ru-central1.internal",
		NetworkSettingsType:    "SOFTWARE_ACCELERATED",
		SchedulingPreemptible:  true,
		ReservedInstancePoolID: "rip0000000000000001",
		AttachedDisks: []domain.AttachedDisk{
			{DiskID: "epd-boot", IsBoot: true, Mode: domain.AttachedDiskModeReadWrite, DeviceName: "boot", AutoDelete: true},
			{DiskID: "epd-data", IsBoot: false, Mode: domain.AttachedDiskModeReadOnly, DeviceName: "data"},
		},
		NetworkInterfaces: []domain.NetworkInterface{
			{Index: "0", NICID: "nic-1", SubnetID: "sub-1", PrimaryV4Address: "10.0.0.2"},
		},
	}

	out := protoconv.Instance(in)
	require.NotNil(t, out)

	assert.Equal(t, in.ID, out.GetId())
	assert.Equal(t, in.ProjectID, out.GetProjectId())
	// created_at truncated to whole seconds (Kachō timestamp convention).
	assert.Equal(t, created.Truncate(time.Second).Unix(), out.GetCreatedAt().GetSeconds())
	assert.Zero(t, out.GetCreatedAt().GetNanos(), "created_at must truncate sub-second precision")
	assert.Equal(t, in.Name, out.GetName())
	assert.Equal(t, in.Description, out.GetDescription())
	assert.Equal(t, in.Labels, out.GetLabels())
	assert.Equal(t, in.ZoneID, out.GetZoneId())
	assert.Equal(t, in.PlatformID, out.GetPlatformId())
	require.NotNil(t, out.GetResources())
	assert.Equal(t, in.Cores, out.GetResources().GetCores())
	assert.Equal(t, in.Memory, out.GetResources().GetMemory())
	assert.Equal(t, in.CoreFraction, out.GetResources().GetCoreFraction())
	assert.Equal(t, in.GPUs, out.GetResources().GetGpus())
	assert.Equal(t, computev1.Instance_Status(in.Status), out.GetStatus())
	assert.Equal(t, in.Metadata, out.GetMetadata())
	assert.Equal(t, in.ServiceAccountID, out.GetServiceAccountId())
	assert.Equal(t, in.FQDN, out.GetFqdn())
	assert.Equal(t, in.ReservedInstancePoolID, out.GetReservedInstancePoolId())
	require.NotNil(t, out.GetSchedulingPolicy())
	assert.True(t, out.GetSchedulingPolicy().GetPreemptible())
	require.NotNil(t, out.GetNetworkSettings())

	// boot disk vs secondary split.
	require.NotNil(t, out.GetBootDisk())
	assert.Equal(t, "epd-boot", out.GetBootDisk().GetDiskId())
	require.Len(t, out.GetSecondaryDisks(), 1)
	assert.Equal(t, "epd-data", out.GetSecondaryDisks()[0].GetDiskId())
	require.Len(t, out.GetNetworkInterfaces(), 1)
	assert.Equal(t, "10.0.0.2", out.GetNetworkInterfaces()[0].GetPrimaryV4Address().GetAddress())
}

// TestInstanceMessage_HasNoHostPlacementField — encodes the proto-contract removal
// at the descriptor level: the public computev1.Instance message must not declare a
// host_id / host_group_id field. If a future proto bump re-introduces one, this
// fails, forcing a conscious security decision (Internal-only vs public) before
// protoconv can project it.
func TestInstanceMessage_HasNoHostPlacementField(t *testing.T) {
	fields := (&computev1.Instance{}).ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		name := string(fields.Get(i).Name())
		require.NotEqual(t, "host_id", name, "public Instance must not expose host_id (infra-sensitive, Internal-only)")
		require.NotEqual(t, "host_group_id", name, "public Instance must not expose host_group_id (infra-sensitive, Internal-only)")
		require.False(t, strings.Contains(name, "host_id") || strings.Contains(name, "host_group"),
			"suspicious host-placement field on public Instance: %q", name)
	}
}
