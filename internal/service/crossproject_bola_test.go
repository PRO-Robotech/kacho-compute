// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
	"github.com/PRO-Robotech/kacho-compute/internal/ports/portmock"
)

// Cross-project BOLA hardening (sec-hardening-r8): a caller holding editor on
// their OWN project must NOT be able to reference a SOURCE resource (disk /
// snapshot / image) owned by ANOTHER project. repo.Get resolves by primary key
// across all projects, and only the caller's project is FGA-Checked, so an
// unguarded source-resolution lets a victim project's data be copied into the
// attacker's project (data exfiltration) or a victim's disk be taken over by an
// attacker's instance (cross-project disk takeover + auto_delete destruction).
//
// The reject MUST be NotFound with the SAME message a genuinely-missing id
// yields (mapRefErr) — an attacker must not get an existence oracle telling
// "this id exists but is in another project".

// ---- (1) source-copy exfiltration: Snapshot / Image / Disk doCreate ----

func TestSnapshot_Create_CrossProjectDisk_NotFound(t *testing.T) {
	svc, _, diskRepo, ops := newSnapshotSvc(t, true)
	// victim's disk (project "victim"), attacker calls with project "attacker".
	diskRepo.Seed(&domain.Disk{ID: "dvic", ProjectID: "victim", Size: diskSizeMin * 5, Status: domain.DiskStatusReady})
	op, err := svc.Create(context.Background(), CreateSnapshotReq{ProjectID: "attacker", DiskID: "dvic", Name: "steal"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "cross-project snapshot source must be denied")
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Disk dvic not found", done.Error.Message, "must be indistinguishable from a missing id (no oracle)")
}

func TestImage_Create_CrossProjectDisk_NotFound(t *testing.T) {
	svc, _, diskRepo, _, ops := newImageSvc(t, true)
	diskRepo.Seed(&domain.Disk{ID: "dvic", ProjectID: "victim", Status: domain.DiskStatusReady})
	op, err := svc.Create(context.Background(), CreateImageReq{ProjectID: "attacker", Name: "img", DiskID: "dvic"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Disk dvic not found", done.Error.Message)
}

func TestImage_Create_CrossProjectSnapshot_NotFound(t *testing.T) {
	svc, _, _, snapRepo, ops := newImageSvc(t, true)
	snapRepo.Seed(&domain.Snapshot{ID: "svic", ProjectID: "victim", DiskSize: diskSizeMin})
	op, err := svc.Create(context.Background(), CreateImageReq{ProjectID: "attacker", Name: "img", SnapshotID: "svic"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Snapshot svic not found", done.Error.Message)
}

func TestImage_Create_CrossProjectImage_NotFound(t *testing.T) {
	svc, repo, _, _, ops := newImageSvc(t, true)
	repo.Seed(&domain.Image{ID: "ivic", ProjectID: "victim", MinDiskSize: diskSizeMin, StorageSize: diskSizeMin})
	op, err := svc.Create(context.Background(), CreateImageReq{ProjectID: "attacker", Name: "img", ImageID: "ivic"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Image ivic not found", done.Error.Message)
}

func TestDisk_Create_CrossProjectImage_NotFound(t *testing.T) {
	svc, _, imgRepo, _, ops := newDiskSvc(t, true)
	imgRepo.Seed(&domain.Image{ID: "ivic", ProjectID: "victim", MinDiskSize: diskSizeMin})
	op, err := svc.Create(context.Background(), CreateDiskReq{ProjectID: "attacker", ZoneID: "ru-central1-a", Size: diskSizeMin * 4, ImageID: "ivic"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Image ivic not found", done.Error.Message)
}

func TestDisk_Create_CrossProjectSnapshot_NotFound(t *testing.T) {
	svc, _, _, snapRepo, ops := newDiskSvc(t, true)
	snapRepo.Seed(&domain.Snapshot{ID: "svic", ProjectID: "victim", DiskSize: diskSizeMin})
	op, err := svc.Create(context.Background(), CreateDiskReq{ProjectID: "attacker", ZoneID: "ru-central1-a", Size: diskSizeMin * 4, SnapshotID: "svic"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Snapshot svic not found", done.Error.Message)
}

// Positive control: same-project source still works (no over-rejection).
func TestDisk_Create_SameProjectSnapshot_OK(t *testing.T) {
	svc, _, _, snapRepo, ops := newDiskSvc(t, true)
	snapRepo.Seed(&domain.Snapshot{ID: "sok", ProjectID: "f", DiskSize: diskSizeMin})
	op, err := svc.Create(context.Background(), CreateDiskReq{ProjectID: "f", ZoneID: "ru-central1-a", Size: diskSizeMin * 4, SnapshotID: "sok"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error, "same-project snapshot source must still succeed")
}

// ---- (2) cross-project disk takeover: Instance.AttachDisk + Create ----

func TestInstance_AttachDisk_CrossProjectDisk_NotFound(t *testing.T) {
	svc, repo, diskRepo, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning) // instance project "f"
	// victim's disk, same zone + READY so ONLY the project guard can reject it.
	diskRepo.Seed(&domain.Disk{ID: "dvic", ProjectID: "victim", ZoneID: "ru-central1-a", Status: domain.DiskStatusReady})
	op, err := svc.AttachDisk(context.Background(), "epdvm1", DiskSourceSpec{DiskID: "dvic", DeviceName: "data0"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "attaching a cross-project disk must be denied")
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Disk dvic not found", done.Error.Message)
}

func TestInstance_AttachDisk_SameProjectDisk_OK(t *testing.T) {
	svc, repo, diskRepo, _, ops := newInstanceSvc(t, true)
	seedRunningInstance(repo, domain.InstanceStatusRunning)
	diskRepo.Seed(&domain.Disk{ID: "dok", ProjectID: "f", ZoneID: "ru-central1-a", Status: domain.DiskStatusReady})
	op, err := svc.AttachDisk(context.Background(), "epdvm1", DiskSourceSpec{DiskID: "dok", DeviceName: "data0"})
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error, "same-project attach must still succeed")
}

func TestInstance_Create_CrossProjectExistingDisk_NotFound(t *testing.T) {
	svc, _, diskRepo, _, ops := newInstanceSvc(t, true)
	diskRepo.Seed(&domain.Disk{ID: "dvic", ProjectID: "victim", ZoneID: "ru-central1-a", Status: domain.DiskStatusReady})
	req := baseCreateReq() // project "f"
	req.BootDisk = DiskSourceSpec{DiskID: "dvic"}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "instance create referencing a cross-project existing disk must be denied")
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Disk dvic not found", done.Error.Message)
}

func TestInstance_Create_CrossProjectInlineImage_NotFound(t *testing.T) {
	svc, _, _, imgRepo, ops := newInstanceSvc(t, true)
	imgRepo.Seed(&domain.Image{ID: "ivic", ProjectID: "victim", MinDiskSize: diskSizeMin})
	req := baseCreateReq() // project "f"
	req.BootDisk = DiskSourceSpec{NewDiskSizeBytes: diskSizeMin, NewSourceImage: "ivic"}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "inline boot disk from a cross-project image must be denied")
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Image ivic not found", done.Error.Message)
}

func TestInstance_Create_CrossProjectInlineSnapshot_NotFound(t *testing.T) {
	// newInstanceSvc does not expose the snapshotRepo, so wire explicitly.
	diskRepo := portmock.NewDiskRepo()
	imgRepo := portmock.NewImageRepo()
	snapRepo := portmock.NewSnapshotRepo()
	instanceRepo := portmock.NewInstanceRepo().WithDiskRepo(diskRepo)
	ops := portmock.NewOpsRepo()
	svc := NewInstanceService(instanceRepo, diskRepo, imgRepo, snapRepo, portmock.NewDiskTypeRepo(), portmock.NewZoneRegistry(),
		&portmock.ProjectClient{OK: true}, portmock.NewNicClient(), ops)
	snapRepo.Seed(&domain.Snapshot{ID: "svic", ProjectID: "victim", DiskSize: diskSizeMin})
	req := baseCreateReq()
	req.BootDisk = DiskSourceSpec{NewDiskSizeBytes: diskSizeMin, NewSourceSnap: "svic"}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.NotNil(t, done.Error, "inline boot disk from a cross-project snapshot must be denied")
	require.Equal(t, int32(codes.NotFound), done.Error.Code)
	require.Equal(t, "Snapshot svic not found", done.Error.Message)
}

func TestInstance_Create_SameProjectInlineImage_OK(t *testing.T) {
	svc, _, _, imgRepo, ops := newInstanceSvc(t, true)
	imgRepo.Seed(&domain.Image{ID: "iok", ProjectID: "f", MinDiskSize: diskSizeMin})
	req := baseCreateReq() // project "f"
	req.BootDisk = DiskSourceSpec{NewDiskSizeBytes: diskSizeMin, NewSourceImage: "iok"}
	op, err := svc.Create(context.Background(), req)
	require.NoError(t, err)
	done := portmock.AwaitOpDone(t, ops, op.ID)
	require.Nil(t, done.Error, "same-project inline image source must still succeed")
}
