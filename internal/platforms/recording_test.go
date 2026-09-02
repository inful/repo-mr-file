package platforms

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRecordingClient_RecordsCalls(t *testing.T) {
	r := &recordingClient{}
	ctx := context.Background()

	_, _ = r.GetProject(ctx, "foo/bar")
	_, _ = r.GetBranch(ctx, "foo/bar", "main")
	_, _ = r.GetFile(ctx, "foo/bar", "ca.pem", "main")
	_ = r.CreateFile(ctx, "foo/bar", "branch", "ca.pem", "main", "commit", strings.NewReader("content"))
	_ = r.UpdateFile(ctx, "foo/bar", "branch", "ca.pem", "main", "commit", "lastid", strings.NewReader("content"))
	_, _ = r.ListOpenMR(ctx, "foo/bar", "src", "tgt")
	_, _ = r.CreateMR(ctx, "foo/bar", CreateMRInput{SourceBranch: "src", TargetBranch: "tgt", Title: "t", Description: "d"})

	if len(r.calls) != 7 {
		t.Fatalf("len(calls) = %d, want 7", len(r.calls))
	}
	wantMethods := []string{"GetProject", "GetBranch", "GetFile", "CreateFile", "UpdateFile", "ListOpenMR", "CreateMR"}
	for i, want := range wantMethods {
		if r.calls[i].method != want {
			t.Errorf("call[%d].method = %q, want %q", i, r.calls[i].method, want)
		}
	}
}

func TestRecordingClient_AllMethodsReturnNilNoError(t *testing.T) {
	r := &recordingClient{}
	ctx := context.Background()

	if _, err := r.GetProject(ctx, "x"); err != nil {
		t.Errorf("GetProject err = %v, want nil", err)
	}
	if exists, err := r.GetBranch(ctx, "x", "y"); err != nil || exists {
		t.Errorf("GetBranch = (%v, %v), want (false, nil)", exists, err)
	}
	if f, err := r.GetFile(ctx, "x", "y", "z"); err != nil || f != nil {
		t.Errorf("GetFile = (%v, %v), want (nil, nil)", f, err)
	}
	if err := r.CreateFile(ctx, "x", "y", "z", "main", "msg", strings.NewReader("c")); err != nil {
		t.Errorf("CreateFile err = %v, want nil", err)
	}
	if err := r.UpdateFile(ctx, "x", "y", "z", "main", "msg", "id", strings.NewReader("c")); err != nil {
		t.Errorf("UpdateFile err = %v, want nil", err)
	}
	if mr, err := r.ListOpenMR(ctx, "x", "y", "z"); err != nil || mr != nil {
		t.Errorf("ListOpenMR = (%v, %v), want (nil, nil)", mr, err)
	}
	if mr, err := r.CreateMR(ctx, "x", CreateMRInput{}); err != nil || mr != nil {
		t.Errorf("CreateMR = (%v, %v), want (nil, nil)", mr, err)
	}
}

func TestAlwaysFailingClient_AllMethodsReturnError(t *testing.T) {
	sentinel := New(KindConfig, "synthetic", errors.New("test error"))
	a := NewAlwaysFailingClient(sentinel).(*AlwaysFailingClient)
	ctx := context.Background()

	if _, err := a.GetProject(ctx, "x"); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("GetProject = %v, want %v", err, sentinel)
	}
	if _, err := a.GetBranch(ctx, "x", "y"); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("GetBranch = %v, want %v", err, sentinel)
	}
	if _, err := a.GetFile(ctx, "x", "y", "z"); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("GetFile = %v, want %v", err, sentinel)
	}
	if err := a.CreateFile(ctx, "x", "y", "z", "main", "m", strings.NewReader("c")); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("CreateFile = %v, want %v", err, sentinel)
	}
	if err := a.UpdateFile(ctx, "x", "y", "z", "main", "m", "id", strings.NewReader("c")); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("UpdateFile = %v, want %v", err, sentinel)
	}
	if _, err := a.ListOpenMR(ctx, "x", "y", "z"); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("ListOpenMR = %v, want %v", err, sentinel)
	}
	if _, err := a.CreateMR(ctx, "x", CreateMRInput{}); err == nil || !errors.Is(err, sentinel) {
		t.Errorf("CreateMR = %v, want %v", err, sentinel)
	}
}

// TestAlwaysFailingClient_EmbeddableForCounting ensures the type can
// be embedded from a different package (e.g. main_test.go's
// transientCounter) to inherit the error-returning behaviour of 7
// methods while overriding one to count calls. This is the pattern
// the cmd tests use after the errClient/errReturningClient/fakeClient
// collapse, so we lock it in here.
func TestAlwaysFailingClient_EmbeddableForCounting(t *testing.T) {
	sentinel := New(KindTransient, "synthetic", errors.New("transient"))

	// getProjectCounter shadows AlwaysFailingClient.GetProject to
	// count. The other 7 methods are promoted from the embed and
	// return the sentinel error.
	c := &getProjectCounter{
		AlwaysFailingClient: NewAlwaysFailingClient(sentinel).(*AlwaysFailingClient),
		sentinel:            sentinel,
	}
	ctx := context.Background()

	// Overridden method increments the counter and returns the sentinel.
	if _, err := c.GetProject(ctx, "x"); !errors.Is(err, sentinel) {
		t.Errorf("GetProject = %v, want %v", err, sentinel)
	}
	if c.calls != 1 {
		t.Errorf("calls = %d, want 1", c.calls)
	}
	// A promoted method returns the sentinel error.
	if _, err := c.GetBranch(ctx, "x", "y"); !errors.Is(err, sentinel) {
		t.Errorf("GetBranch = %v, want %v", err, sentinel)
	}
}

// getProjectCounter is the test-only type that embeds
// *AlwaysFailingClient and shadows GetProject. Defined as a named
// type (not an inline struct) so the override is a real method on
// the type — that's the pattern cmd/repo-mr-file/main_test.go's
// transientCounter uses too.
type getProjectCounter struct {
	*AlwaysFailingClient
	sentinel error
	calls    int
}

func (c *getProjectCounter) GetProject(_ context.Context, _ string) (*Project, error) {
	c.calls++
	return nil, c.sentinel
}
