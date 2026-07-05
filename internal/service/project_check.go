// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// checkProject — единый cross-service existence-check владельца-Project через
// ProjectClient (kacho-iam ProjectService.Get). Раньше был byte-for-byte
// продублирован в instance/image/disk (метод `checkFolder`) + inline в snapshot;
// сведён в один helper (rule #11), чтобы маппинг (peer-недоступен → Unavailable,
// не-найдено → NotFound) не расходился между ресурсами.
//
// NB: `folder`-словарь и текст ошибки — verbatim-YC контракт (часть API-контракта
// per api-conventions; см. docs/architecture/07-known-divergences.md, r6). Их
// нейминг закрывается координированным де-YC эпиком, не здесь.
func checkProject(ctx context.Context, pc ProjectClient, projectID string) error {
	exists, err := pc.Exists(ctx, projectID)
	if err != nil {
		return status.Error(codes.Unavailable, "folder check: upstream project service unavailable")
	}
	if !exists {
		return status.Errorf(codes.NotFound, "Folder with id %s not found", projectID)
	}
	return nil
}
