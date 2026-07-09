// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// Decision — результат фильтра для конкретного ListObjects-вызова.
//
//   - BypassAll=true: фильтр не применяется (public catalog, cluster-admin,
//     fail-open recovery). repo.List возвращает все строки.
//   - Empty=true: subject ничего не разрешено в этом resource_type — handler
//     должен вернуть пустой response без обращения к repo.
//   - AllowedIDs: explicit-список id, к которым subject имеет access. Используется
//     repo как `WHERE id = ANY($allowed)`.
type Decision struct {
	BypassAll  bool
	Empty      bool
	AllowedIDs []string
	// FromCache — true если ответ получен из cache (для observability и тестов).
	FromCache bool
	// FailOpen — true если решение принято в degraded-mode (FGA error + fail-open).
	FailOpen bool
}

// IsBypass — true если фильтрация не применяется (catalog / fail-open).
func (d Decision) IsBypass() bool { return d.BypassAll }

// IDs — отсортированный allow-list (deterministic ordering for stable pagination).
func (d Decision) IDs() []string { return d.AllowedIDs }

// Filter — port интерфейс. Единственная реализация — FGAFilter (через
// iam.ListObjects); public-catalog / FGA-disabled bypass выражается nil-фильтром
// в handler'е (resolveListFilter) либо Config.Enabled=false, а не отдельным типом.
type Filter interface {
	// ListAllowedIDs возвращает Decision для (subject, resourceType, action).
	// resourceType — FGA object type ("compute_instance", "compute_disk", ...).
	// action — semantic permission ("compute.instances.list", ...) — the iam
	// server resolves the verb ("list" → "viewer", read==enforce) to an FGA
	// relation. subject — FGA subject string ("user:usr_alice"
	// или "service_account:sa_xxx").
	ListAllowedIDs(ctx context.Context, subject, resourceType, action string) (Decision, error)
}

// Config — параметры FGAFilter.
type Config struct {
	// Enabled — master-switch. false → ListAllowedIDs возвращает BypassAll=true
	// (no-op: filter не применяется). Для dev-кластера / graceful start без iam.
	Enabled bool
	// Timeout — per-request deadline к iam.ListObjects.
	Timeout time.Duration
	// CacheTTL — TTL одной записи в in-process decision cache.
	CacheTTL time.Duration
	// CacheMaxEntries — bound для cache size. TTL — первичный механизм вытеснения
	// (записи живут CacheTTL); при превышении bound putCache сбрасывает одну
	// произвольную запись (не LRU — см. putCache; TTL-short делает выбор жертвы
	// несущественным).
	CacheMaxEntries int
	// MaxResults — hard cap on the number of authorized resource ids requested from
	// iam.ListObjects per (subject, resourceType, action). This is the tenant List
	// allow-list size — deliberately DECOUPLED from CacheMaxEntries so that tuning
	// cache memory never silently truncates a tenant's visible resources. iam clamps
	// to the FGA hard cap (10000) regardless, so the default requests that maximum.
	MaxResults int
	// FailOpen — на FGA error: true → BypassAll=true + audit-warn; false → Unavailable.
	FailOpen bool
}

// defaultListMaxResults — FGA hard cap on ListObjects results (proto:
// max_results <= 10000; iam clamps to this). Used as the default allow-list result
// cap, independent of cache sizing.
const defaultListMaxResults = 10000

// DefaultConfig — sane defaults: filter включён, 500ms timeout, 5s TTL, 10000 cache
// entries, 10000 allow-list result cap, fail-closed.
func DefaultConfig() Config {
	return Config{
		Enabled:         true,
		Timeout:         500 * time.Millisecond,
		CacheTTL:        5 * time.Second,
		CacheMaxEntries: 10000,
		MaxResults:      defaultListMaxResults,
		FailOpen:        false,
	}
}

// AuthorizeClient — узкий интерфейс к iam.AuthorizeService (для тестируемости).
// Реализуется *grpcAuthorizeClient (production) либо mock (unit-tests).
//
// Signature deliberately matches the generated AuthorizeServiceClient.ListObjects
// so that NewIAMAuthorizeClient is a thin pass-through.
type AuthorizeClient interface {
	ListObjects(ctx context.Context, in *iamv1.ListObjectsRequest, opts ...grpc.CallOption) (*iamv1.ListObjectsResponse, error)
}

// FGAFilter — продакшен-реализация Filter поверх iam.AuthorizeService.ListObjects
// с in-memory TTL-кешем.
type FGAFilter struct {
	cli AuthorizeClient
	cfg Config

	// now — источник времени для TTL-логики кеша. Инъектируется (default
	// time.Now) чтобы TTL-expiry можно было проверять детерминированно, продвигая
	// фейковые часы, а не через time.Sleep + wall-clock (flaky под нагрузкой CI).
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	decision Decision
	expires  time.Time
}

// NewFGAFilter создаёт фильтр. cli — обычно grpcAuthorizeClient (см. iam_authorize_client.go).
// Если cli == nil — фильтр всегда BypassAll (graceful start без iam).
func NewFGAFilter(cli AuthorizeClient, cfg Config) *FGAFilter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 500 * time.Millisecond
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Second
	}
	if cfg.CacheMaxEntries <= 0 {
		cfg.CacheMaxEntries = 10000
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = defaultListMaxResults
	}
	return &FGAFilter{
		cli:   cli,
		cfg:   cfg,
		now:   time.Now,
		cache: make(map[string]cacheEntry, cfg.CacheMaxEntries),
	}
}

// ListAllowedIDs — основной entry-point.
func (f *FGAFilter) ListAllowedIDs(ctx context.Context, subject, resourceType, action string) (Decision, error) {
	if !f.cfg.Enabled || f.cli == nil {
		return Decision{BypassAll: true}, nil
	}
	if subject == "" {
		// Anonymous caller — fail-closed (handler obtains subject from gRPC metadata).
		return Decision{}, status.Error(codes.Unauthenticated, "list filter: subject required")
	}
	if resourceType == "" || action == "" {
		return Decision{}, fmt.Errorf("authzfilter: resourceType and action required")
	}

	key := cacheKey(subject, resourceType, action)
	if d, ok := f.getCache(key); ok {
		d.FromCache = true
		return d, nil
	}

	// Cache miss — call iam.ListObjects with deadline.
	callCtx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()

	resp, err := f.cli.ListObjects(callCtx, &iamv1.ListObjectsRequest{
		Subject:      subject,
		ResourceType: resourceType,
		Action:       action,
		MaxResults:   int64(f.cfg.MaxResults),
	})
	if err != nil {
		return f.handleErr(err)
	}

	ids := append([]string(nil), resp.GetResourceIds()...)
	sort.Strings(ids) // deterministic ordering for stable pagination

	d := Decision{
		AllowedIDs: ids,
		Empty:      len(ids) == 0,
	}
	f.putCache(key, d)
	return d, nil
}

// handleErr — выбор reaction по fail-open / fail-closed.
func (f *FGAFilter) handleErr(err error) (Decision, error) {
	if f.cfg.FailOpen {
		return Decision{BypassAll: true, FailOpen: true}, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Decision{}, status.Errorf(codes.Unavailable, "list filter: iam.ListObjects deadline exceeded after %s", f.cfg.Timeout)
	}
	// Preserve gRPC status code if upstream returned one; else wrap as Unavailable.
	if s, ok := status.FromError(err); ok && s.Code() != codes.OK && s.Code() != codes.Unknown {
		return Decision{}, status.Errorf(codes.Unavailable, "list filter: iam.ListObjects %s: %s", s.Code(), s.Message())
	}
	return Decision{}, status.Errorf(codes.Unavailable, "list filter: iam.ListObjects: %v", err)
}

func cacheKey(subject, resourceType, action string) string {
	return subject + "|" + resourceType + "|" + action
}

func (f *FGAFilter) getCache(key string) (Decision, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache[key]
	if !ok {
		return Decision{}, false
	}
	if f.now().After(e.expires) {
		delete(f.cache, key)
		return Decision{}, false
	}
	// Return a shallow copy with a fresh slice header to avoid caller mutation.
	d := e.decision
	if len(d.AllowedIDs) > 0 {
		idsCopy := make([]string, len(d.AllowedIDs))
		copy(idsCopy, d.AllowedIDs)
		d.AllowedIDs = idsCopy
	}
	return d, true
}

func (f *FGAFilter) putCache(key string, d Decision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Only evict when inserting a NEW key at capacity — refreshing an
	// already-present key must not drop a different subject's live decision
	// (net map shrink → victim re-incurs an iam RTT / fail-closed). Parity with
	// sibling ProjectClient.putExists (!present && len>=max).
	if _, present := f.cache[key]; !present && len(f.cache) >= f.cfg.CacheMaxEntries {
		// Naive eviction: drop a random pre-existing entry (Go map iteration order).
		// Acceptable for short TTL — entries expire within 5s anyway.
		for k := range f.cache {
			delete(f.cache, k)
			break
		}
	}
	f.cache[key] = cacheEntry{
		decision: d,
		expires:  f.now().Add(f.cfg.CacheTTL),
	}
}

// Size — текущий размер cache (для observability/tests).
func (f *FGAFilter) Size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cache)
}
