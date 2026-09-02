package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inful/repo-mr-file/internal/platforms"
)

// fakeClient is a Client that returns a fixed Project and a configurable
// error from every method. Used to drive the bundler through error paths
// without needing a real GitLab server.
type fakeClient struct {
	project *platforms.Project
	err     error
}

func (f *fakeClient) GetProject(context.Context, string) (*platforms.Project, error) {
	return f.project, f.err
}
func (f *fakeClient) GetBranch(context.Context, string, string) (bool, error) {
	return false, f.err
}
func (f *fakeClient) GetFile(context.Context, string, string, string) (*platforms.File, error) {
	return nil, f.err
}
func (f *fakeClient) CreateFile(context.Context, string, string, string, string, io.Reader) error {
	return f.err
}
func (f *fakeClient) UpdateFile(context.Context, string, string, string, string, string, io.Reader) error {
	return f.err
}
func (f *fakeClient) ListOpenMR(context.Context, string, string, string) (*platforms.MergeRequest, error) {
	return nil, f.err
}
func (f *fakeClient) CreateMR(context.Context, string, platforms.CreateMRInput) (*platforms.MergeRequest, error) {
	return nil, f.err
}

// recordingProjectClient is a Client that records calls and returns
// realistic values. Used for end-to-end success tests.
type recordingProjectClient struct {
	project *platforms.Project
}

func (r *recordingProjectClient) GetProject(_ context.Context, _ string) (*platforms.Project, error) {
	return r.project, nil
}
func (r *recordingProjectClient) GetBranch(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
func (r *recordingProjectClient) GetFile(_ context.Context, _, _, _ string) (*platforms.File, error) {
	return nil, platforms.New(platforms.KindNotFound, "GetFile", errors.New("not found"))
}
func (r *recordingProjectClient) CreateFile(_ context.Context, _, _, _, _ string, _ io.Reader) error {
	return nil
}
func (r *recordingProjectClient) UpdateFile(_ context.Context, _, _, _, _, _ string, _ io.Reader) error {
	return nil
}
func (r *recordingProjectClient) ListOpenMR(_ context.Context, _, _, _ string) (*platforms.MergeRequest, error) {
	return nil, nil
}
func (r *recordingProjectClient) CreateMR(_ context.Context, _ string, _ platforms.CreateMRInput) (*platforms.MergeRequest, error) {
	return &platforms.MergeRequest{IID: 1, WebURL: "https://gitlab.example.com/foo/bar/-/merge_requests/1"}, nil
}

// transientCounter counts how many times GetProject is called and returns
// the configured error from every call. Used to assert retry semantics.
type transientCounter struct {
	calls atomic.Int64
	err   error
}

func (t *transientCounter) GetProject(context.Context, string) (*platforms.Project, error) {
	t.calls.Add(1)
	return nil, t.err
}
func (t *transientCounter) GetBranch(context.Context, string, string) (bool, error) {
	return false, t.err
}
func (t *transientCounter) GetFile(context.Context, string, string, string) (*platforms.File, error) {
	return nil, t.err
}
func (t *transientCounter) CreateFile(context.Context, string, string, string, string, io.Reader) error {
	return t.err
}
func (t *transientCounter) UpdateFile(context.Context, string, string, string, string, string, io.Reader) error {
	return t.err
}
func (t *transientCounter) ListOpenMR(context.Context, string, string, string) (*platforms.MergeRequest, error) {
	return nil, t.err
}
func (t *transientCounter) CreateMR(context.Context, string, platforms.CreateMRInput) (*platforms.MergeRequest, error) {
	return nil, t.err
}

// stubRunDeps builds a temp bundle file and returns args that point run() at it.
func stubRunDeps(t *testing.T) ([]string, string) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bundle, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{
		"--tag=v1.2.3",
		"--repo=foo/bar",
		"--target-path=ca.pem",
		"--source-path=" + bundle,
		"--api-token=tk",
	}, bundle
}

func TestRun_ExitCodeMapping(t *testing.T) {
	args, _ := stubRunDeps(t)

	cases := []struct {
		name     string
		kind     platforms.Kind
		wantCode int
	}{
		{"config", platforms.KindConfig, 2},
		{"auth", platforms.KindAuth, 3},
		{"notfound", platforms.KindNotFound, 4},
		{"conflict", platforms.KindConflict, 5},
		{"transient", platforms.KindTransient, 6},
		{"unknown", platforms.KindUnknown, 7},
		{"internal", platforms.KindInternal, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeClient{err: platforms.New(tc.kind, "GetProject", errors.New("synthetic"))}
			var stdout, stderr bytes.Buffer
			got := run(args, &stdout, &stderr, fake)
			if got != tc.wantCode {
				t.Errorf("exit code = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

func TestRun_Success_DryRun(t *testing.T) {
	args, _ := stubRunDeps(t)
	args = append(args, "--dry-run")

	var stdout, stderr bytes.Buffer
	got := run(args, &stdout, &stderr, nil)
	if got != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "https://example.invalid/dry-run") {
		t.Errorf("stdout missing MR URL; got: %q", stdout.String())
	}
}

func TestRun_Live_Success_WithRecordingClient(t *testing.T) {
	args, _ := stubRunDeps(t)
	client := &recordingProjectClient{project: &platforms.Project{ID: 1, DefaultBranch: "main"}}
	var stdout, stderr bytes.Buffer
	got := run(args, &stdout, &stderr, client)
	if got != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "https://gitlab.example.com/foo/bar/-/merge_requests/1") {
		t.Errorf("stdout missing MR URL; got: %q", stdout.String())
	}
}

func TestRun_BadArgs_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{"--repo=foo/bar"}, &stdout, &stderr, nil)
	if got != 2 {
		t.Errorf("exit code = %d, want 2 for missing --tag", got)
	}
}

func TestRun_InvalidLogFormat_Exit2(t *testing.T) {
	args, _ := stubRunDeps(t)
	args = append(args, "--log-format=xml")
	var stdout, stderr bytes.Buffer
	got := run(args, &stdout, &stderr, nil)
	if got != 2 {
		t.Errorf("exit code = %d, want 2 for invalid --log-format", got)
	}
}

func TestRun_BundleFileMissing_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{
		"--tag=t",
		"--repo=foo/bar",
		"--target-path=ca.pem",
		"--source-path=/nonexistent/file.pem",
		"--api-token=tk",
	}, &stdout, &stderr, nil)
	if got != 2 {
		t.Errorf("exit code = %d, want 2 for missing bundle file", got)
	}
}

func TestRun_RetriesSemantics(t *testing.T) {
	fc := &transientCounter{err: platforms.New(platforms.KindTransient, "GetProject", errors.New("503"))}
	args, _ := stubRunDeps(t)
	args = append(args, "--retries=3")

	var stdout, stderr bytes.Buffer
	_ = run(args, &stdout, &stderr, fc)
	if got := fc.calls.Load(); got != 4 {
		t.Errorf("GetProject calls = %d, want 4 (1 initial + 3 retries)", got)
	}
}
