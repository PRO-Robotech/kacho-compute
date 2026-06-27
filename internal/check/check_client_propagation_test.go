package check

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	iamv1 "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"
)

// fakeInternalIAM — recording-stub InternalIAMServiceServer для проверки
// outgoing-MD wrapping в IAMCheckClient.Check (W1.4 / KAC-140) — зеркало
// kacho-vpc check_client_propagation_test.go.
type fakeInternalIAM struct {
	iamv1.UnimplementedInternalIAMServiceServer

	mu      sync.Mutex
	lastMD  metadata.MD
	lastReq *iamv1.CheckRequest
	resp    *iamv1.CheckResponse
}

func (f *fakeInternalIAM) Check(ctx context.Context, req *iamv1.CheckRequest) (*iamv1.CheckResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		f.lastMD = md.Copy()
	}
	f.lastReq = req
	if f.resp == nil {
		return &iamv1.CheckResponse{Allowed: true}, nil
	}
	return f.resp, nil
}

func startFakeInternalIAM(t *testing.T, fake *fakeInternalIAM) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	iamv1.RegisterInternalIAMServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestIAMCheckClient_Check_PropagatesPrincipal — W1.4 §3.6
// PROP-COMPUTE-CHECK-01 (mirror of vpc PROP-VPC-CHECK-01).
func TestIAMCheckClient_Check_PropagatesPrincipal(t *testing.T) {
	fake := &fakeInternalIAM{}
	conn := startFakeInternalIAM(t, fake)
	client := NewIAMCheckClient(conn)
	require.NotNil(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = operations.WithPrincipal(ctx, operations.Principal{
		Type:        "user",
		ID:          "usr_alice",
		DisplayName: "alice@example.com",
	})

	allowed, err := client.Check(ctx, "user:usr_alice", "compute.instances.get", "compute_instance:epd_xxx")
	require.NoError(t, err)
	assert.True(t, allowed)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.NotNil(t, fake.lastMD, "stub must have captured incoming MD")

	if got := fake.lastMD.Get(grpcsrv.MDKeyPrincipalType); len(got) != 1 || got[0] != "user" {
		t.Errorf("x-kacho-principal-type = %v, want [user]", got)
	}
	if got := fake.lastMD.Get(grpcsrv.MDKeyPrincipalID); len(got) != 1 || got[0] != "usr_alice" {
		t.Errorf("x-kacho-principal-id = %v, want [usr_alice]", got)
	}
	if got := fake.lastMD.Get(grpcsrv.MDKeyPrincipalDisplay); len(got) != 1 || got[0] != "alice@example.com" {
		t.Errorf("x-kacho-principal-display-name = %v, want [alice@example.com]", got)
	}

	require.NotNil(t, fake.lastReq)
	assert.Equal(t, "user:usr_alice", fake.lastReq.SubjectId)
	assert.Equal(t, "compute.instances.get", fake.lastReq.Relation)
	assert.Equal(t, "compute_instance:epd_xxx", fake.lastReq.Object)
}

// TestIAMCheckClient_Check_SystemPrincipalFallback — W1.4 §3.6 mirror of
// PROP-VPC-CHECK-02: empty ctx → SystemPrincipal fallback headers ставятся
// (auth.PropagateOutgoing семантика: PrincipalFromContext fallback'ит на
// SystemPrincipal, который непуст → форсятся headers).
func TestIAMCheckClient_Check_SystemPrincipalFallback(t *testing.T) {
	fake := &fakeInternalIAM{}
	conn := startFakeInternalIAM(t, fake)
	client := NewIAMCheckClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := client.Check(ctx, "user:bootstrap", "viewer", "compute_instance:epd_xxx")
	require.NoError(t, err)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.NotNil(t, fake.lastMD)
	if got := fake.lastMD.Get(grpcsrv.MDKeyPrincipalID); len(got) != 1 || got[0] != "bootstrap" {
		t.Errorf("x-kacho-principal-id = %v, want [bootstrap] (SystemPrincipal fallback)", got)
	}
	if got := fake.lastMD.Get(grpcsrv.MDKeyPrincipalType); len(got) != 1 || got[0] != "system" {
		t.Errorf("x-kacho-principal-type = %v, want [system]", got)
	}
}
