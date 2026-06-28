// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import (
	"time"

	computev1 "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
)

// SnapshotStatus — состояние снапшота (control-plane: всегда READY после Create).
type SnapshotStatus int

// Значения SnapshotStatus зеркалят computev1.Snapshot_Status.
const (
	SnapshotStatusUnspecified SnapshotStatus = iota
	SnapshotStatusCreating
	SnapshotStatusReady
	SnapshotStatusError
	SnapshotStatusDeleting
)

// Snapshot — снапшот (folder-level ресурс). source_disk_id обязателен в Create
// (не FK — семантика Kachō допускает удаление source-диска). disk_size/storage_size
// фиксируются на момент создания (= disk.size) и immutable.
type Snapshot struct {
	ID                 string
	ProjectID          string
	CreatedAt          time.Time
	Name               string
	Description        string
	Labels             map[string]string
	StorageSize        int64
	DiskSize           int64
	ProductIDs         []string
	Status             SnapshotStatus
	SourceDiskID       string
	HardwareGeneration *computev1.HardwareGeneration
	KMSKey             *computev1.KMSKey
}
