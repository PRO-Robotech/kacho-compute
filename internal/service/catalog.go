// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-compute/internal/domain"
)

// ---- DiskTypeService (read-only public + admin CRUD через Internal* handler) ----

// DiskTypeService — read/CRUD доступ к справочнику типов дисков.
type DiskTypeService struct {
	repo DiskTypeRepo
}

// NewDiskTypeService создаёт DiskTypeService.
func NewDiskTypeService(repo DiskTypeRepo) *DiskTypeService { return &DiskTypeService{repo: repo} }

// Get возвращает DiskType по id.
func (s *DiskTypeService) Get(ctx context.Context, id string) (*domain.DiskType, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_type_id required")
	}
	t, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return t, nil
}

// List возвращает все типы дисков.
func (s *DiskTypeService) List(ctx context.Context, p Pagination) ([]*domain.DiskType, string, error) {
	return s.repo.List(ctx, p)
}

// Create создаёт тип диска (admin-only).
func (s *DiskTypeService) Create(ctx context.Context, id, description string, zoneIDs []string) (*domain.DiskType, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	t, err := s.repo.Insert(ctx, &domain.DiskType{ID: id, Description: description, ZoneIDs: zoneIDs})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return t, nil
}

// Update обновляет тип диска (admin-only).
func (s *DiskTypeService) Update(ctx context.Context, id, description string, zoneIDs []string) (*domain.DiskType, error) {
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "disk_type_id required")
	}
	t, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	t.Description = description
	t.ZoneIDs = zoneIDs
	updated, err := s.repo.Update(ctx, t)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return updated, nil
}

// Delete удаляет тип диска (admin-only).
func (s *DiskTypeService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return status.Error(codes.InvalidArgument, "disk_type_id required")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return mapRepoErr(err)
	}
	return nil
}
