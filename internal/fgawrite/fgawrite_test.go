package fgawrite

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type recordingWriter struct {
	calls [][3]string
	err   error
}

func (w *recordingWriter) WriteHierarchyTuple(_ context.Context, objectType, objectID, projectID string) error {
	w.calls = append(w.calls, [3]string{objectType, objectID, projectID})
	return w.err
}

// TestEmit_NilWriter — a nil writer is a no-op (OpenFGA tuple-write not wired).
func TestEmit_NilWriter(t *testing.T) {
	assert.NotPanics(t, func() {
		Emit(context.Background(), nil, nil, "compute_instance", "epd_1", "prj_1")
	})
}

// TestEmit_Delegates — a configured writer receives the resource→project tuple.
func TestEmit_Delegates(t *testing.T) {
	w := &recordingWriter{}
	Emit(context.Background(), w, nil, "compute_disk", "epd_d1", "prj_1")
	assert.Equal(t, [][3]string{{"compute_disk", "epd_d1", "prj_1"}}, w.calls)
}

// TestEmit_AllComputeTypes — sanity that every compute_* FGA type can be emitted.
func TestEmit_AllComputeTypes(t *testing.T) {
	w := &recordingWriter{}
	for _, objType := range []string{"compute_instance", "compute_disk", "compute_image", "compute_snapshot"} {
		Emit(context.Background(), w, nil, objType, "id-"+objType, "prj_x")
	}
	assert.Len(t, w.calls, 4)
}

// TestEmit_EmptyID_Skipped — an empty object/project id never reaches the writer
// (a dangling `<type>:` object would otherwise be created).
func TestEmit_EmptyID_Skipped(t *testing.T) {
	w := &recordingWriter{}
	Emit(context.Background(), w, nil, "compute_instance", "", "prj_1")
	Emit(context.Background(), w, nil, "compute_instance", "epd_1", "")
	assert.Empty(t, w.calls, "empty id must be skipped before the writer")
}

// TestEmit_WriterError_NonFatal — a writer failure is swallowed (the resource
// row is already committed; an Operation must not fail on an FGA hiccup).
func TestEmit_WriterError_NonFatal(t *testing.T) {
	w := &recordingWriter{err: errors.New("openfga down")}
	assert.NotPanics(t, func() {
		Emit(context.Background(), w, nil, "compute_snapshot", "fd8_s", "prj_s")
	})
	assert.Len(t, w.calls, 1)
}
