package platforms

import (
	"context"
	"io"
	"math/rand"
	"sync"
	"time"
)

// recordingClient is a Client implementation that records each call and
// returns nil values. It powers --dry-run mode and unit tests.
type recordingClient struct {
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	method string
	args   []any
}

// Calls returns a snapshot of recorded calls in the order they happened.
func (r *recordingClient) Calls() []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recordingClient) record(method string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedCall{method: method, args: args})
}

func (r *recordingClient) GetProject(_ context.Context, repoPath string) (*Project, error) {
	r.record("GetProject", repoPath)
	return nil, nil
}

func (r *recordingClient) GetBranch(_ context.Context, repoPath, branch string) (bool, error) {
	r.record("GetBranch", repoPath, branch)
	return false, nil
}

func (r *recordingClient) GetFile(_ context.Context, repoPath, filePath, ref string) (*File, error) {
	r.record("GetFile", repoPath, filePath, ref)
	return nil, nil
}

func (r *recordingClient) CreateFile(_ context.Context, repoPath, branch, filePath, startBranch, commitMsg string, _ io.Reader) error {
	r.record("CreateFile", repoPath, branch, filePath, commitMsg, startBranch)
	return nil
}

func (r *recordingClient) UpdateFile(_ context.Context, repoPath, branch, filePath, commitMsg, lastCommitID string, _ io.Reader) error {
	r.record("UpdateFile", repoPath, branch, filePath, commitMsg, lastCommitID)
	return nil
}

func (r *recordingClient) ListOpenMR(_ context.Context, repoPath, sourceBranch, targetBranch string) (*MergeRequest, error) {
	r.record("ListOpenMR", repoPath, sourceBranch, targetBranch)
	return nil, nil
}

func (r *recordingClient) CreateMR(_ context.Context, repoPath string, in CreateMRInput) (*MergeRequest, error) {
	r.record("CreateMR", repoPath, in)
	return nil, nil
}

// NewRecordingClient returns a Client that records every call. Used by
// --dry-run and by tests that want to assert which methods were invoked.
func NewRecordingClient() Client {
	return &recordingClient{}
}

// ---------------------------------------------------------------------------
// Package-private RNG for jitter, seeded lazily. Tests should pass their own
// *rand.Rand via RetryConfig.Rand for determinism.
// ---------------------------------------------------------------------------

var (
	defaultRandMu  sync.Mutex
	defaultRandSrc *rand.Rand
	defaultRandNow = time.Now().UnixNano()
)

func defaultRandFloat() float64 {
	defaultRandMu.Lock()
	defer defaultRandMu.Unlock()
	if defaultRandSrc == nil {
		defaultRandSrc = rand.New(rand.NewSource(defaultRandNow))
	}
	return defaultRandSrc.Float64()
}
