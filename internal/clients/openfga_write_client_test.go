package clients

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenFGAWriteClient_WriteHierarchyTuple_OK — KAC-188 follow-up: a successful
// write posts the expected `compute_<type>:<id>#project@project:<pid>` tuple.
func TestOpenFGAWriteClient_WriteHierarchyTuple_OK(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/stores/store-1/write", r.URL.Path)
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &OpenFGAWriteClient{Endpoint: hostPort(srv.URL), StoreID: "store-1", ModelID: "m-1"}
	err := c.WriteHierarchyTuple(context.Background(), "compute_instance", "epd_abc", "prj_xyz")
	require.NoError(t, err)

	var req fgaWriteRequest
	require.NoError(t, json.Unmarshal(gotBody, &req))
	require.Len(t, req.Writes.TupleKeys, 1)
	tk := req.Writes.TupleKeys[0]
	assert.Equal(t, "project:prj_xyz", tk.User)
	assert.Equal(t, "project", tk.Relation)
	assert.Equal(t, "compute_instance:epd_abc", tk.Object)
	assert.Equal(t, "m-1", req.AuthorizationModelID)
}

// TestOpenFGAWriteClient_Idempotent — a 400 "already exists" is a success
// (the desired tuple state is already reached).
func TestOpenFGAWriteClient_Idempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"write_failed_due_to_invalid_input","message":"cannot write a tuple which already exists"}`))
	}))
	defer srv.Close()

	c := &OpenFGAWriteClient{Endpoint: hostPort(srv.URL), StoreID: "store-1"}
	err := c.WriteHierarchyTuple(context.Background(), "compute_disk", "epd_d1", "prj_1")
	require.NoError(t, err, "already-exists 400 must be treated as idempotent success")
}

// TestOpenFGAWriteClient_Retry5xx — a transient 5xx is retried, then succeeds.
func TestOpenFGAWriteClient_Retry5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &OpenFGAWriteClient{Endpoint: hostPort(srv.URL), StoreID: "store-1"}
	err := c.WriteHierarchyTuple(context.Background(), "compute_image", "fd8_i1", "prj_a")
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "5xx must be retried once")
}

// TestOpenFGAWriteClient_PermanentError — a non-idempotent 4xx is not retried.
func TestOpenFGAWriteClient_PermanentError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid relation"}`))
	}))
	defer srv.Close()

	c := &OpenFGAWriteClient{Endpoint: hostPort(srv.URL), StoreID: "store-1"}
	err := c.WriteHierarchyTuple(context.Background(), "compute_snapshot", "fd8_s", "prj_g")
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "non-idempotent 4xx must not be retried")
}

// TestOpenFGAWriteClient_NotConfigured — a zero-value client errors clearly.
func TestOpenFGAWriteClient_NotConfigured(t *testing.T) {
	c := &OpenFGAWriteClient{}
	err := c.WriteHierarchyTuple(context.Background(), "compute_instance", "epd_x", "prj_x")
	require.Error(t, err)
}

// hostPort strips the http:// scheme from an httptest server URL.
func hostPort(url string) string {
	return url[len("http://"):]
}
