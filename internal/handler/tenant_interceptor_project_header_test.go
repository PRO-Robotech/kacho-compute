// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"
)

// TestTenantFromMetadata_ReadsProjectIDHeader locks the post-KAC-106 wire
// contract: caller project-scope is carried by the canonical `x-kacho-project-id`
// header (matching the renamed project_id model and the kacho-vpc sibling), NOT
// the vestigial `x-kacho-folder-id`. Reading the wrong header name silently drops
// the defense-in-depth ownership scope (ProjectIDs empty → full access).
func TestTenantFromMetadata_ReadsProjectIDHeader(t *testing.T) {
	md := metadata.New(map[string]string{
		"x-kacho-project-id": "p1",
	})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	trusted := tenantFromMetadata(ctx, true)
	assert.Contains(t, trusted.ProjectIDs, "p1",
		"trusted peer's x-kacho-project-id must populate ProjectIDs")
	assert.True(t, trusted.HasProjectAccess("p1"),
		"caller scoped to p1 has access to p1")
	assert.False(t, trusted.HasProjectAccess("p-other"),
		"caller scoped to p1 must not have access to a different project")
}

// TestTenantFromMetadata_IgnoresLegacyFolderHeader ensures the legacy
// `x-kacho-folder-id` header is no longer honoured — it must not grant scope.
func TestTenantFromMetadata_IgnoresLegacyFolderHeader(t *testing.T) {
	md := metadata.New(map[string]string{
		"x-kacho-folder-id": "f1",
	})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	trusted := tenantFromMetadata(ctx, true)
	assert.Empty(t, trusted.ProjectIDs,
		"legacy x-kacho-folder-id must not populate ProjectIDs")
}
