package platforms

import (
	"context"
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
