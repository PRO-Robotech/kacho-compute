// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package clients содержит gRPC-адаптеры к peer-сервисам (Clean Architecture
// outbound adapters): kacho-iam (ProjectService) и kacho-vpc
// (Subnet/SecurityGroup/Address). Реализуют port-интерфейсы из internal/ports.
//
// peer для project-existence-check — kacho-iam.ProjectService.Get.
//
// outgoing ctx обёрнут `auth.PropagateOutgoing` — peer-call несёт
// `x-kacho-principal-*` MD, чтобы iam-side scope-filter увидел реального caller'а
// (иначе — anonymous/system, NOT_FOUND, тихий fail Operation).
package clients

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// projectExistsTTL — TTL положительного результата Exists.
const projectExistsTTL = 30 * time.Second

// defaultProjectCallTimeout — per-call deadline applied to EVERY
// iam.ProjectService.Get attempt (audit-r6 P1). Mirrors defaultZoneCallTimeout's
// rationale: retry.OnUnavailable only classifies codes.Unavailable, so an
// app-slow (not down) iam peer that never responds would hang the caller for the
// lifetime of the inbound ctx (LRO worker opTimeout) on every Create/Move
// hot-path call. Package default const (no configurable knob exists for this
// edge yet), per architecture.md's documented fallback.
const defaultProjectCallTimeout = 5 * time.Second

// ProjectClient реализует service.ProjectClient через gRPC к kacho-iam
// с TTL-кешем для Exists (hot path: каждый Create/Move).
type ProjectClient struct {
	cli iamv1.ProjectServiceClient
	// timeout — per-call deadline applied to every Get attempt
	// (defaultProjectCallTimeout unless overridden, e.g. test seam via direct
	// field assignment).
	timeout time.Duration

	mu     sync.RWMutex
	exists map[string]time.Time
}

// NewProjectClient создаёт ProjectClient.
func NewProjectClient(conn *grpc.ClientConn) *ProjectClient {
	return &ProjectClient{
		cli:     iamv1.NewProjectServiceClient(conn),
		exists:  make(map[string]time.Time),
		timeout: defaultProjectCallTimeout,
	}
}

// NewProjectClientWith создаёт ProjectClient поверх готового
// iamv1.ProjectServiceClient (seam для unit-тестов с fake-клиентом), паритет с
// clients.NewGeoClientWith.
func NewProjectClientWith(cli iamv1.ProjectServiceClient) *ProjectClient {
	return &ProjectClient{
		cli:     cli,
		exists:  make(map[string]time.Time),
		timeout: defaultProjectCallTimeout,
	}
}

// Exists проверяет существование Project через kacho-iam.ProjectService.Get.
// Положительный результат кешируется на projectExistsTTL (убирает gRPC RTT
// из hot-path при burst-нагрузке). NotFound НЕ кешируется (свеже-созданный
// project быстро становится виден).
//
// Каждая попытка (в т.ч. каждый retry.OnUnavailable-повтор) несёт собственный
// context.WithTimeout(c.timeout) — app-slow iam (peer жив, но не отвечает) бьётся
// per-call deadline'ом, а не висит до inbound ctx (worker opTimeout), см. audit-r6 P1.
func (c *ProjectClient) Exists(ctx context.Context, projectID string) (bool, error) {
	c.mu.RLock()
	exp, ok := c.exists[projectID]
	c.mu.RUnlock()
	if ok && time.Now().Before(exp) {
		return true, nil
	}

	var exists bool
	err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		_, rerr := c.cli.Get(auth.PropagateOutgoing(callCtx), &iamv1.GetProjectRequest{ProjectId: projectID})
		if rerr != nil {
			st, ok := status.FromError(rerr)
			if ok && st.Code() == codes.NotFound {
				exists = false
				return nil
			}
			return rerr
		}
		exists = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if exists {
		c.mu.Lock()
		c.exists[projectID] = time.Now().Add(projectExistsTTL)
		c.mu.Unlock()
	}
	return exists, nil
}

// NoopProjectClient — заглушка для KACHO_COMPUTE_SKIP_PEER_VALIDATION=true
// (Exists всегда true) и для unit/newman без поднятого kacho-iam.
type NoopProjectClient struct{}

// Exists всегда возвращает (true, nil).
func (NoopProjectClient) Exists(_ context.Context, _ string) (bool, error) { return true, nil }
