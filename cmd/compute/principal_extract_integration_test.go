package main

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// TestCompute_PublicChain_ExtractsPrincipalFromMD — KAC-178 §2 (W1.4 mirror
// of kacho-vpc). Verifies that the public gRPC chain assembled in main.go's
// runServe() includes grpcsrv.UnaryPrincipalExtract() as the first interceptor.
// Без него operations.PrincipalFromContext(ctx) внутри любого handler видит
// SystemPrincipal() = user:bootstrap вместо реального caller'а, форварднутого
// api-gateway через x-kacho-principal-* MD → Operation.created_by = "anonymous".
//
// Тест воспроизводит chain main.go (порядок ВАЖЕН: principal-extract → tenant);
// при изменении composition в runServe — chain здесь нужно держать в sync.
func TestCompute_PublicChain_ExtractsPrincipalFromMD(t *testing.T) {
	// Recording handler — фиксирует Principal, который видит handler через
	// operations.PrincipalFromContext после прохода через цепочку.
	var (
		mu             sync.Mutex
		observedPrinc  operations.Principal
		handlerInvoked bool
	)
	recordingInterceptor := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		mu.Lock()
		observedPrinc = operations.PrincipalFromContext(ctx)
		handlerInvoked = true
		mu.Unlock()
		return handler(ctx, req)
	}

	// Public chain — порядок mirror'ит main.go: principal-extract ПЕРВЫМ.
	// Recording interceptor СТАВИМ ПОСЛЕДНИМ, чтобы он видел уже-extract'ed Principal.
	publicUnary := []grpc.UnaryServerInterceptor{
		grpcsrv.UnaryPrincipalExtract(),
		recordingInterceptor,
	}

	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(publicUnary...))
	healthpb.RegisterHealthServer(srv, health.NewServer()) // лёгкий handler для probe-вызова
	t.Cleanup(srv.GracefulStop)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Шлём health-check с x-kacho-principal-* MD — api-gateway-style.
	cli := healthpb.NewHealthClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(),
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-test-alice",
		grpcsrv.MDKeyPrincipalDisplay, "alice@example.com",
	)
	_, err = cli.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.True(t, handlerInvoked, "recording interceptor должен был запуститься")
	assert.Equal(t, "user", observedPrinc.Type, "Principal.Type должен быть extracted из MD")
	assert.Equal(t, "usr-test-alice", observedPrinc.ID, "Principal.ID должен быть extracted из MD")
	assert.Equal(t, "alice@example.com", observedPrinc.DisplayName, "Principal.DisplayName должен быть extracted из MD")
}

// TestCompute_PublicChain_FallsBackToSystem_WhenNoMD — convenience-проверка
// negative-case: если api-gateway по какой-то причине НЕ форварднул MD
// (graceful-degradation, dev), Principal должен fall-back на SystemPrincipal
// и не паниковать.
func TestCompute_PublicChain_FallsBackToSystem_WhenNoMD(t *testing.T) {
	var (
		mu             sync.Mutex
		observedPrinc  operations.Principal
		handlerInvoked bool
	)
	recordingInterceptor := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		mu.Lock()
		observedPrinc = operations.PrincipalFromContext(ctx)
		handlerInvoked = true
		mu.Unlock()
		return handler(ctx, req)
	}

	publicUnary := []grpc.UnaryServerInterceptor{
		grpcsrv.UnaryPrincipalExtract(),
		recordingInterceptor,
	}

	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(publicUnary...))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	t.Cleanup(srv.GracefulStop)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	cli := healthpb.NewHealthClient(conn)
	_, err = cli.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.True(t, handlerInvoked)
	assert.Equal(t, "system", observedPrinc.Type, "fallback Type")
	assert.Equal(t, "bootstrap", observedPrinc.ID, "fallback ID")
}
