// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package clients — iam_register_applier.go.
//
// IAMRegisterApplier is the register-drainer Applier (corelib outbox/drainer):
// it replays a decoded FGA register/unregister intent by calling kacho-iam
// InternalIAMService.RegisterResource / UnregisterResource over (optionally) mTLS.
//
// kacho-compute никогда не ходит в FGA напрямую; FGA спрятан за IAM.
// The applier is the ONLY place compute reaches IAM's FGA-proxy, and it does so
// asynchronously off the Operation hot-path (intent durable in the outbox), so
// an IAM outage cannot lose the owner-tuple nor fail the resource mutation
// (no best-effort dual-write).
//
// Idempotency contract: RegisterResource / UnregisterResource return gRPC
// OK on a repeated tuple — the drainer can retry safely (at-least-once → exactly
// applied). The applier maps gRPC status → drainer disposition:
//   - OK                         → nil (drainer marks sent_at).
//   - codes.InvalidArgument      → drainer.ErrPermanent (poison; malformed tuple,
//     e.g. poison classification).
//   - any other (Unavailable,…)  → transient (drainer retries with backoff).
package clients

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
	iamv1 "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-compute/internal/fgaintent"
)

// IAMRegisterClient — the narrow slice of InternalIAMService the applier needs.
// Port lives next to the adapter so the drainer/applier can be unit-tested with a
// fake recorder (no grpc). iamv1.InternalIAMServiceClient satisfies it.
type IAMRegisterClient interface {
	RegisterResource(ctx context.Context, in *iamv1.RegisterResourceRequest, opts ...grpc.CallOption) (*iamv1.RegisterResourceResponse, error)
	UnregisterResource(ctx context.Context, in *iamv1.UnregisterResourceRequest, opts ...grpc.CallOption) (*iamv1.UnregisterResourceResponse, error)
}

// IAMRegisterApplier applies fgaintent.Payload register/unregister intents to
// kacho-iam. Build with NewIAMRegisterApplier; use Apply as the drainer.Applier.
type IAMRegisterApplier struct {
	cli IAMRegisterClient
}

// NewIAMRegisterApplier wraps an InternalIAMService client connection.
func NewIAMRegisterApplier(conn *grpc.ClientConn) *IAMRegisterApplier {
	return &IAMRegisterApplier{cli: iamv1.NewInternalIAMServiceClient(conn)}
}

// NewIAMRegisterApplierWithClient injects an IAMRegisterClient directly (tests).
func NewIAMRegisterApplierWithClient(cli IAMRegisterClient) *IAMRegisterApplier {
	return &IAMRegisterApplier{cli: cli}
}

// Apply implements drainer.Applier[fgaintent.Payload]. It registers (or
// unregisters) every tuple in the intent. A whole set is one logical
// RegisterResource transaction; if any tuple call errors transiently the whole
// row is retried (idempotent re-apply of already-applied tuples → OK).
func (a *IAMRegisterApplier) Apply(ctx context.Context, eventType string, p fgaintent.Payload) error {
	if len(p.Tuples) == 0 {
		// Empty intent — nothing to do; treat as applied (do not poison/retry).
		return nil
	}
	// Propagate principal MD so IAM-side mTLS-SA / audit sees the caller (parity
	// with the other peer-calls; in dev this is anonymous/system).
	ctx = auth.PropagateOutgoing(ctx)
	srcVer := sourceVersionPB(p.SourceVersion)
	switch eventType {
	case fgaintent.EventRegister:
		for _, tpl := range p.Tuples {
			// RSAB β: forward the owner labels + parent-scope so kacho-iam can
			// populate its output-only resource_mirror (label+parent sync). Fields
			// are additive/optional — empty values mirror gracefully.
			// β-hardening: forward source_version (monotonic per-object) so the
			// mirror UPSERT is last-source-state-wins (stale reordered intent → no-op).
			if _, err := a.cli.RegisterResource(ctx, &iamv1.RegisterResourceRequest{
				SubjectId:       tpl.SubjectID,
				Relation:        tpl.Relation,
				Object:          tpl.Object,
				Labels:          p.Labels,
				ParentProjectId: p.ParentProjectID,
				ParentAccountId: p.ParentAccountID,
				SourceVersion:   srcVer,
			}); err != nil {
				return classifyApplyErr(err)
			}
		}
		return nil
	case fgaintent.EventUnregister:
		for _, tpl := range p.Tuples {
			// Symmetry: Unregister removes the mirror row by object; the mirror
			// fields are carried for message-shape symmetry but IAM uses only object
			// + source_version (tombstone-version: a stale tombstone won't wipe a
			// fresher row — β-hardening).
			if _, err := a.cli.UnregisterResource(ctx, &iamv1.UnregisterResourceRequest{
				SubjectId:       tpl.SubjectID,
				Relation:        tpl.Relation,
				Object:          tpl.Object,
				Labels:          p.Labels,
				ParentProjectId: p.ParentProjectID,
				ParentAccountId: p.ParentAccountID,
				SourceVersion:   srcVer,
			}); err != nil {
				return classifyApplyErr(err)
			}
		}
		return nil
	default:
		return errors.Join(drainer.ErrPermanent, fmt.Errorf("unknown fga intent event_type %q", eventType))
	}
}

// sourceVersionPB converts the decoded payload source_version to a proto
// Timestamp. A zero time (legacy payload / old outbox row) → nil, which IAM
// treats as '-infinity' (applies unconditionally — back-compat).
func sourceVersionPB(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// classifyApplyErr maps a gRPC status to the drainer disposition. InvalidArgument
// is a permanent poison (malformed tuple — retry is pointless); every other code
// (notably Unavailable from IAM-down or an mTLS handshake mismatch) is transient →
// drainer retries with backoff (intent stays durable).
func classifyApplyErr(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.InvalidArgument {
		return errors.Join(drainer.ErrPermanent, err)
	}
	return err
}
