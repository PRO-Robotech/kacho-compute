// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package handler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	genstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
)

// fakeOwnedOpsRepo — тестовый double, реализующий operations.Repo И
// operations.OwnedOperationRepo. Хранит операции вместе с principal'ом и
// применяет ownership-предикат в GetOwned/CancelOwned (зеркало pgRepo-семантики
// из corelib). Дополнительно записывает owner каждого owned-вызова — это нужно,
// чтобы доказать, что владелец резолвится ИСКЛЮЧИТЕЛЬНО из ctx-principal'а
// (anti-spoof), а не из тела/заголовков запроса.
type fakeOwnedOpsRepo struct {
	mu           sync.Mutex
	ops          map[string]*operations.Operation
	getOwners    []operations.Owner
	cancelOwners []operations.Owner
	forceErr     error // если задана — repo-вызовы возвращают её (симуляция INTERNAL)
}

func newFakeOwnedOpsRepo() *fakeOwnedOpsRepo {
	return &fakeOwnedOpsRepo{ops: make(map[string]*operations.Operation)}
}

func (f *fakeOwnedOpsRepo) seed(op *operations.Operation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *op
	f.ops[op.ID] = &cp
}

func ownerMatches(op *operations.Operation, owner operations.Owner) bool {
	return op.Principal.Type == owner.PrincipalType && op.Principal.ID == owner.PrincipalID
}

// ---- operations.OwnedOperationRepo ----

func (f *fakeOwnedOpsRepo) GetOwned(_ context.Context, id string, owner operations.Owner) (*operations.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getOwners = append(f.getOwners, owner)
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	op, ok := f.ops[id]
	if !ok || !ownerMatches(op, owner) {
		return nil, operations.ErrNotFound
	}
	cp := *op
	return &cp, nil
}

func (f *fakeOwnedOpsRepo) CancelOwned(_ context.Context, id string, owner operations.Owner) (*operations.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelOwners = append(f.cancelOwners, owner)
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	op, ok := f.ops[id]
	if !ok || !ownerMatches(op, owner) {
		return nil, operations.ErrNotFound
	}
	if op.Done {
		if op.Error != nil && op.Error.GetCode() == 1 {
			cp := *op
			return &cp, nil // идемпотентно: уже CANCELLED
		}
		return nil, operations.ErrAlreadyDone // terminal SUCCESS/ERROR
	}
	op.Done = true
	op.Error = &genstatus.Status{Code: 1, Message: "operation cancelled"}
	op.ModifiedAt = time.Now().UTC()
	cp := *op
	return &cp, nil
}

// ---- operations.Repo ----

func (f *fakeOwnedOpsRepo) Create(_ context.Context, op operations.Operation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := op
	f.ops[op.ID] = &cp
	return nil
}

func (f *fakeOwnedOpsRepo) CreateWithPrincipal(_ context.Context, op operations.Operation, p operations.Principal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op.Principal = p
	cp := op
	f.ops[op.ID] = &cp
	return nil
}

func (f *fakeOwnedOpsRepo) Get(_ context.Context, id string) (*operations.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	op, ok := f.ops[id]
	if !ok {
		return nil, operations.ErrNotFound
	}
	cp := *op
	return &cp, nil
}

func (f *fakeOwnedOpsRepo) List(_ context.Context, _ operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}

func (f *fakeOwnedOpsRepo) MarkDone(_ context.Context, id string, resp *anypb.Any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.Response = resp
	return nil
}

func (f *fakeOwnedOpsRepo) MarkError(_ context.Context, id string, st *genstatus.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.Error = st
	return nil
}

func (f *fakeOwnedOpsRepo) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, ok := f.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	if op.Done {
		return operations.ErrAlreadyDone
	}
	op.Done = true
	op.Error = &genstatus.Status{Code: 1, Message: "operation cancelled"}
	return nil
}

var (
	_ operations.Repo               = (*fakeOwnedOpsRepo)(nil)
	_ operations.OwnedOperationRepo = (*fakeOwnedOpsRepo)(nil)
)

// opUserCtx — ctx с доверенным principal'ом user/<id> (как после JWT-валидации
// на gateway, проброшенной через principal-extract).
func opUserCtx(id string) context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: id, DisplayName: "test"})
}

// opSACtx — ctx с principal service_account/<id>.
func opSACtx(id string) context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "service_account", ID: id, DisplayName: "test"})
}

// opAdminCtx — ctx cluster-admin'а (TenantCtx.Admin=true, как после доверенного
// x-kacho-admin). tenantCtxKey — package-private ключ tenant_interceptor.go.
func opAdminCtx() context.Context {
	return context.WithValue(context.Background(), tenantCtxKey{}, TenantCtx{Admin: true})
}

// seedInFlight — кладёт in-flight (done=false) операцию владельца (type,id).
func seedInFlight(repo *fakeOwnedOpsRepo, principalType, principalID string) *operations.Operation {
	op := &operations.Operation{
		ID:          ids.NewID(ids.PrefixOperationCompute),
		Description: "unit-test op",
		Principal:   operations.Principal{Type: principalType, ID: principalID, DisplayName: "test"},
	}
	repo.seed(op)
	return op
}

// nonexistentOpID — well-formed (epd-prefix) operation-id, заведомо не созданный.
const nonexistentOpID = "epd00000000000000000"

// --- Валидация аргумента ---

func TestOperationHandler_Get_InvalidArg(t *testing.T) {
	h := NewOperationHandler(newFakeOwnedOpsRepo())
	_, err := h.Get(opUserCtx("usr-A"), &operationpb.GetOperationRequest{OperationId: ""})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Equal(t, "operation_id required", st.Message())
}

func TestOperationHandler_Cancel_InvalidArg(t *testing.T) {
	h := NewOperationHandler(newFakeOwnedOpsRepo())
	_, err := h.Cancel(opUserCtx("usr-A"), &operationpb.CancelOperationRequest{OperationId: ""})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// --- Владелец поллит свою операцию ---

func TestOperationHandler_Get_Owner_OK(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	h := NewOperationHandler(repo)

	got, err := h.Get(opUserCtx("usr-A"), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	assert.Equal(t, op.ID, got.Id)
}

// --- cross-tenant Get → NotFound, no-leak code/template-equality ---

func TestOperationHandler_Get_CrossTenant_NotFound_NoLeak(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	h := NewOperationHandler(repo)

	// usr-B (чужой) на реально существующую op-A.
	_, errNotOwned := h.Get(opUserCtx("usr-B"), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.Error(t, errNotOwned)
	stNotOwned, _ := grpcstatus.FromError(errNotOwned)
	assert.Equal(t, codes.NotFound, stNotOwned.Code())
	assert.Equal(t, "operation "+op.ID+" not found", stNotOwned.Message())

	// usr-B на заведомо несуществующий well-formed id.
	_, errMissing := h.Get(opUserCtx("usr-B"), &operationpb.GetOperationRequest{OperationId: nonexistentOpID})
	require.Error(t, errMissing)
	stMissing, _ := grpcstatus.FromError(errMissing)

	// No-leak: код идентичен, шаблон текста идентичен (расходится только эхо-id).
	assert.Equal(t, stNotOwned.Code(), stMissing.Code(),
		"not-owned и nonexistent должны давать идентичный gRPC-код")
	assert.Equal(t, "operation "+nonexistentOpID+" not found", stMissing.Message())
}

// --- Владелец отменяет in-flight op ---

func TestOperationHandler_Cancel_Owner_OK(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	h := NewOperationHandler(repo)

	got, err := h.Cancel(opUserCtx("usr-A"), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.GetError())
	assert.Equal(t, int32(1), got.GetError().GetCode(), "CANCELLED")
	assert.Equal(t, "operation cancelled", got.GetError().GetMessage())
}

// --- cross-tenant Cancel → NotFound, жертва не мутирована ---

func TestOperationHandler_Cancel_CrossTenant_NotFound_VictimUnmodified(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	h := NewOperationHandler(repo)

	_, err := h.Cancel(opUserCtx("usr-B"), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code())

	// Жертва осталась done=false — её владелец видит in-flight.
	got, err := h.Get(opUserCtx("usr-A"), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	assert.False(t, got.Done, "чужой Cancel не должен мутировать операцию")
}

// --- повторный Cancel владельцем уже-CANCELLED → OK (идемпотентно) ---

func TestOperationHandler_Cancel_Idempotent(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	h := NewOperationHandler(repo)

	_, err := h.Cancel(opUserCtx("usr-A"), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.NoError(t, err)

	got, err := h.Cancel(opUserCtx("usr-A"), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.NoError(t, err, "повторная отмена уже-CANCELLED — не ошибка")
	assert.True(t, got.Done)
	require.NotNil(t, got.GetError())
	assert.Equal(t, int32(1), got.GetError().GetCode())
}

// --- Cancel завершённой успехом op → FailedPrecondition ---

func TestOperationHandler_Cancel_AlreadyCompleted_FailedPrecondition(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := &operations.Operation{
		ID:        ids.NewID(ids.PrefixOperationCompute),
		Principal: operations.Principal{Type: "user", ID: "usr-A"},
		Done:      true, // терминальный SUCCESS (Error пуст)
	}
	repo.seed(op)
	h := NewOperationHandler(repo)

	_, err := h.Cancel(opUserCtx("usr-A"), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Equal(t, "operation "+op.ID+" already completed", st.Message())
}

// --- cluster-admin short-circuit ---

func TestOperationHandler_Admin_ShortCircuit(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	h := NewOperationHandler(repo)

	// admin Get чужой op → OK.
	got, err := h.Get(opAdminCtx(), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	assert.Equal(t, op.ID, got.Id)

	// admin Cancel чужой in-flight op → OK, терминал CANCELLED.
	cancelled, err := h.Cancel(opAdminCtx(), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	assert.True(t, cancelled.Done)
	require.NotNil(t, cancelled.GetError())
	assert.Equal(t, int32(1), cancelled.GetError().GetCode())

	// non-admin не-владелец по-прежнему NotFound на ту же op.
	_, err = h.Get(opUserCtx("usr-B"), &operationpb.GetOperationRequest{OperationId: op.ID})
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code())
}

// --- service-account владелец — match по паре (type,id) ---

func TestOperationHandler_Owner_TypePlusID(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "service_account", "X")
	h := NewOperationHandler(repo)

	// Тот же (type,id) — владелец.
	_, err := h.Get(opSACtx("X"), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.NoError(t, err)

	// Тот же id-суффикс, но другой principal_type — НЕ владелец.
	_, err = h.Get(opUserCtx("X"), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code(),
		"ownership сравнивает пару (type,id), не только id")
}

// --- owner резолвится ТОЛЬКО из доверенного ctx (anti-spoof) ---

func TestOperationHandler_OwnerFromTrustedCtx_NotSpoofable(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	h := NewOperationHandler(repo)

	_, err := h.Get(opUserCtx("usr-B"), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code())

	// Repo получил owner ровно из ctx-principal'а (usr-B), а не из op/тела/заголовков.
	require.Len(t, repo.getOwners, 1)
	assert.Equal(t, operations.Owner{PrincipalType: "user", PrincipalID: "usr-B"}, repo.getOwners[0],
		"owner должен прийти из доверенного ctx-principal'а, не из запроса")
}

// --- INTERNAL без leak'а pgx-detail ---

func TestOperationHandler_RepoError_InternalNoLeak(t *testing.T) {
	repo := newFakeOwnedOpsRepo()
	op := seedInFlight(repo, "user", "usr-A")
	repo.forceErr = errors.New("pgx: dial tcp 10.0.0.1:5432: connection refused")
	h := NewOperationHandler(repo)

	_, gErr := h.Get(opUserCtx("usr-A"), &operationpb.GetOperationRequest{OperationId: op.ID})
	require.Error(t, gErr)
	gSt, _ := grpcstatus.FromError(gErr)
	assert.Equal(t, codes.Internal, gSt.Code())
	assert.Equal(t, "operation get failed", gSt.Message())
	assert.False(t, strings.Contains(gSt.Message(), "pgx"), "pgx-detail не должен течь наружу")

	_, cErr := h.Cancel(opUserCtx("usr-A"), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.Error(t, cErr)
	cSt, _ := grpcstatus.FromError(cErr)
	assert.Equal(t, codes.Internal, cSt.Code())
	assert.Equal(t, "operation cancel failed", cSt.Message())
}
