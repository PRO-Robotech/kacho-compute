// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"
)

// TestTenantFromMetadata_AdminProjectRequireTrust — authz-влияющие заголовки
// x-kacho-admin / x-kacho-project-id читаются ТОЛЬКО от trust-gated peer'а
// (verified client-cert / trusted forwarder на mTLS-листенере; insecure-листенер
// = back-compat trusted). Untrusted peer (TLS без verified cert, не trusted
// forwarder) не должен уметь подделать admin/project-scope, т.к. эти заголовки
// не связаны с verified peer-identity (audit SEC: CWE-807/CWE-639).
// x-kacho-actor (audit-only, не влияет на authz) читается всегда.
func TestTenantFromMetadata_AdminProjectRequireTrust(t *testing.T) {
	md := metadata.New(map[string]string{
		"x-kacho-admin":      "true",
		"x-kacho-project-id": "p1",
		"x-kacho-actor":      "someone",
	})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// Untrusted peer → admin/project DROPPED (anonymous для authz), actor сохранён.
	untrusted := tenantFromMetadata(ctx, false)
	assert.False(t, untrusted.Admin, "untrusted peer must not gain admin from raw x-kacho-admin")
	assert.Empty(t, untrusted.ProjectIDs, "untrusted peer must not gain project-scope from raw x-kacho-project-id")
	assert.True(t, untrusted.IsAnonymous(), "untrusted peer with forged headers is anonymous for authz")
	assert.Equal(t, "someone", untrusted.Actor, "actor is audit-only, read regardless of trust")

	// Trusted peer (gateway / insecure back-compat) → headers honoured.
	trusted := tenantFromMetadata(ctx, true)
	assert.True(t, trusted.Admin, "trusted peer's x-kacho-admin is honoured")
	assert.Contains(t, trusted.ProjectIDs, "p1", "trusted peer's x-kacho-project-id is honoured")
	assert.Equal(t, "someone", trusted.Actor)
}
