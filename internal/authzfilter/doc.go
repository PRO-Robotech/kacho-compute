// Package authzfilter — FGA-filtered List integration (KAC-127 Phase 4).
//
// Provides ListAllowedIDs(ctx, principal, resourceType, action) wrapping
// kacho-iam.AuthorizeService.ListObjects. Caches per-(subject, resourceType,
// action) for short TTL (5s) to amortize FGA RTT for hot list paths.
//
// Fail-closed by default: if FGA is unreachable and no cached entry exists,
// returns Unavailable. KACHO_COMPUTE_LIST_FILTER_FAIL_OPEN=true switches to
// degraded mode — returns nil-filter (i.e. allow all id-checking is bypassed
// and the caller's repo list returns all rows). Audit-log warns on each
// fail-open.
//
// Public catalog (DiskType / Zone / Region) bypasses this filter — anyone
// authenticated can list catalog. Bypass is the caller's responsibility
// (passes BypassFilter() instead of a real filter).
//
// Configurable env vars (all KACHO_COMPUTE_LIST_FILTER_*):
//   - URL/grpc address — re-uses KACHO_COMPUTE_AUTHZ_IAM_GRPC_ADDR.
//   - ENABLED          — master switch (default false; explicit opt-in).
//   - TIMEOUT_MS       — per-request timeout to iam.ListObjects (default 500).
//   - CACHE_TTL_MS     — per-entry TTL in the in-memory decision cache (default 5000).
//   - CACHE_MAX_ENTRIES — bounds the cache size (default 10000).
//   - FAIL_OPEN        — if true, on FGA error returns ALL ids (no filter); else fail-closed.
//
// Clean Architecture: this package is an outbound adapter (calls iam over
// gRPC) and a port (consumed by service-layer List use-cases). It lives in
// internal/ as it's compute-specific glue; if a second service grows the
// same pattern, lift it into kacho-corelib/authz/listobjects.
package authzfilter
