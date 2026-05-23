// Package clients — openfga_write_client.go (KAC-133).
//
// Write-side OpenFGA adapter: publishes the per-resource hierarchy tuple
// (`compute_<resource>:<id>#project@project:<project_id>`) a freshly created
// compute resource needs for the FGA `<rel> from project` cascade to resolve.
//
// kacho-compute previously had only a read-side FGA path (List filter + per-RPC
// Check). This client closes the write side. It talks to the shared cluster
// OpenFGA HTTP API directly — the same store kacho-iam writes its
// account/project tuples into.
//
// Implements internal/fgawrite.HierarchyTupleWriter.
// Mirrors kacho-vpc/internal/clients/openfga_write_client.go (KAC-127 issue #22).
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultFGAWriteTimeout — per-call deadline for an OpenFGA write when the
// composition root passes 0.
const defaultFGAWriteTimeout = 2 * time.Second

// OpenFGAWriteClient — HTTP client for the OpenFGA `POST /stores/{id}/write`
// endpoint, scoped to hierarchy-tuple writes.
//
// A zero value (Endpoint == "" || StoreID == "") makes WriteHierarchyTuple a
// no-op error — but the composition root only wires this client when tuple
// write is configured, and passes nil otherwise (fgawrite.Emit is nil-safe).
type OpenFGAWriteClient struct {
	// Endpoint — host:port of the OpenFGA HTTP API (e.g. "kacho-umbrella-openfga:8080").
	Endpoint string
	// StoreID — OpenFGA store id (shared with kacho-iam).
	StoreID string
	// ModelID — pinned authorization_model_id. Empty → store default.
	ModelID string
	// Timeout — per-call context deadline. Zero → defaultFGAWriteTimeout.
	Timeout time.Duration
	// HTTP — injectable for tests; nil → http.DefaultClient.
	HTTP *http.Client
}

type fgaTupleKey struct {
	User     string `json:"user"`
	Relation string `json:"relation"`
	Object   string `json:"object"`
}

type fgaWriteRequest struct {
	AuthorizationModelID string `json:"authorization_model_id,omitempty"`
	Writes               struct {
		TupleKeys []fgaTupleKey `json:"tuple_keys"`
	} `json:"writes"`
}

// WriteHierarchyTuple writes `<objectType>:<objectID>#project@project:<projectID>`.
//
// Idempotent: OpenFGA returns 400 with a "tuple ... already exists" message
// when the tuple is already present — that is treated as success (the desired
// state is reached). Transient network/5xx failures are retried.
func (c *OpenFGAWriteClient) WriteHierarchyTuple(ctx context.Context, objectType, objectID, projectID string) error {
	if c == nil || c.Endpoint == "" || c.StoreID == "" {
		return fmt.Errorf("openfga write client not configured")
	}

	reqBody := fgaWriteRequest{AuthorizationModelID: c.ModelID}
	reqBody.Writes.TupleKeys = []fgaTupleKey{{
		User:     fmt.Sprintf("project:%s", projectID),
		Relation: "project",
		Object:   fmt.Sprintf("%s:%s", objectType, objectID),
	}}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("openfga write marshal: %w", err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultFGAWriteTimeout
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	url := fmt.Sprintf("http://%s/stores/%s/write", c.Endpoint, c.StoreID)

	return retryHTTPWrite(ctx, timeout, func(ctx context.Context) error {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if rerr != nil {
			return rerr
		}
		req.Header.Set("Content-Type", "application/json")
		resp, derr := httpc.Do(req)
		if derr != nil {
			return fmt.Errorf("openfga write: %w", derr)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Idempotent: a 400 whose body mentions an already-existing tuple is
		// the desired end state — not a failure.
		if resp.StatusCode == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(string(payload)), "already exists") {
			return nil
		}
		// 5xx is transient → retryable; 4xx (other than the above) is permanent.
		if resp.StatusCode >= 500 {
			return retryableErr{fmt.Errorf("openfga write: status %d: %s", resp.StatusCode, payload)}
		}
		return fmt.Errorf("openfga write: status %d: %s", resp.StatusCode, payload)
	})
}

// retryableErr marks an error as worth retrying inside retryHTTPWrite.
type retryableErr struct{ error }

// retryHTTPWrite runs fn with up to 3 attempts, retrying only retryableErr,
// each attempt bounded by timeout. Mirrors the corelib retry budget shape.
func retryHTTPWrite(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	const attempts = 3
	var lastErr error
	for i := 0; i < attempts; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		err := fn(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if _, ok := err.(retryableErr); !ok {
			return err
		}
		// Exponential backoff: 100ms, 200ms before the 2nd / 3rd attempt.
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(100*(1<<i)) * time.Millisecond):
			}
		}
	}
	return lastErr
}
