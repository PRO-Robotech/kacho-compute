// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package repo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/kacho-compute/internal/service"
)

// isUniqueViolation — Postgres unique-constraint violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	s := err.Error()
	return strings.Contains(s, "23505") || strings.Contains(s, "duplicate key value")
}

// isAttachedDisksDiskIDUniqViolation — true если 23505 пришла именно на индекс
// `attached_disks_disk_id_uniq` (миграция 0007). Используется для
// отделения «диск уже attached к другой Instance» (FailedPrecondition) от
// общего AlreadyExists по другим UNIQUE-constraint.
func isAttachedDisksDiskIDUniqViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code != "23505" {
			return false
		}
		return pgErr.ConstraintName == "attached_disks_disk_id_uniq"
	}
	return strings.Contains(err.Error(), "attached_disks_disk_id_uniq")
}

// isAttachedDisksDeviceOrBootUniqViolation — true если 23505 пришла на
// per-instance partial-UNIQUE `attached_disks_device_uniq` (дубль непустого
// device_name на одном instance) или `attached_disks_boot_uniq` (второй
// boot-disk). Эти инварианты sequential-путь (service.AttachDisk software-loop)
// отбивает как FailedPrecondition; concurrent-путь (mutateAndReload) должен
// маппиться так же — а не в generic AlreadyExists, — чтобы error-контракт был
// одинаков на обоих путях (audit: sequential vs DB-backstop code mismatch).
func isAttachedDisksDeviceOrBootUniqViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code != "23505" {
			return false
		}
		return pgErr.ConstraintName == "attached_disks_device_uniq" ||
			pgErr.ConstraintName == "attached_disks_boot_uniq"
	}
	s := err.Error()
	return strings.Contains(s, "attached_disks_device_uniq") ||
		strings.Contains(s, "attached_disks_boot_uniq")
}

// isFKViolation — Postgres foreign_key_violation (SQLSTATE 23503). Возникает на
// Delete Disk пока он attached (FK attached_disks.disk_id RESTRICT). Маппится в
// gRPC FailedPrecondition ("The disk is being used").
func isFKViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	s := err.Error()
	return strings.Contains(s, "23503") || strings.Contains(s, "violates foreign key")
}

// wrapPgErr классифицирует pgx-ошибку и возвращает sentinel-ошибку из
// service-пакета. НЕ leak'ает raw PG-сообщение клиенту: неизвестные классы → ErrInternal.
//
// kind/id — для NotFound сообщений (имя ресурса + id).
func wrapPgErr(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if id != "" {
			return fmt.Errorf("%w: %s %s not found", service.ErrNotFound, kind, id)
		}
		return service.ErrNotFound
	}
	if isUniqueViolation(err) {
		return service.ErrAlreadyExists
	}
	if isFKViolation(err) {
		return fmt.Errorf("%w: The %s %s is being used", service.ErrFailedPrecondition, strings.ToLower(kind), id)
	}
	return service.ErrInternal
}
