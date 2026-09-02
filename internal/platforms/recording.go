package platforms

import (
	"context"
	"io"
	"sync"
)

// recordingClient is a Client implementation that records each call and
// returns nil values. It powers --dry-run mode and unit tests.
type recordingClient struct {
	mu       sync.Mutex
	recorded []recordedCall
}

type recordedCall struct {
	method string
	args   []any
}

func (r *recordingClient) record(method string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recorded = append(r.recorded, recordedCall{method: method, args: args})
}

func (r *recordingClient) GetProject(_ context.Context, repoPath string) (*Project, error) {
	r.record("GetProject", repoPath)
	return nil, nil
}

func (r *recordingClient) GetBranch(_ context.Context, repoPath, branch string) (bool, error) {
	r.record("GetBranch", repoPath, branch)
	return false, nil
}

func (r *recordingClient) CreateBranch(_ context.Context, repoPath, newBranch, startBranch string) error {
	r.record("CreateBranch", repoPath, newBranch, startBranch)
	return nil
}

func (r *recordingClient) GetFile(_ context.Context, repoPath, filePath, ref string) (*File, error) {
	r.record("GetFile", repoPath, filePath, ref)
	return nil, nil
}

func (r *recordingClient) CreateFile(_ context.Context, repoPath, branch, filePath, startBranch, commitMsg string, _ io.Reader) error {
	r.record("CreateFile", repoPath, branch, filePath, commitMsg, startBranch)
	return nil
}

func (r *recordingClient) UpdateFile(_ context.Context, repoPath, branch, filePath, startBranch, commitMsg, lastCommitID string, _ io.Reader) error {
	r.record("UpdateFile", repoPath, branch, filePath, commitMsg, lastCommitID, startBranch)
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

// AlwaysFailingClient is a Client that returns a fixed error from every
// method. Used as the return value when a platform-specific client's
// constructor fails (invalid base URL, missing GitHub username) so the
// caller still sees a well-typed error through the normal flow.
//
// The struct is exported so tests in other packages can embed
// *AlwaysFailingClient to inherit the error-returning behavior of all
// 8 methods while overriding one (e.g. to count calls). The field is
// unexported; callers outside the platforms package construct the type
// via NewAlwaysFailingClient and type-assert back if they need the
// concrete type for embedding.
type AlwaysFailingClient struct {
	err error
}

// NewAlwaysFailingClient returns a Client whose every method returns err.
// Constructors of the real per-platform clients wrap their construction
// errors in this so buildLiveClient can return a platforms.Client value
// even on failure (rather than a nil interface or a panic).
func NewAlwaysFailingClient(err error) Client {
	return &AlwaysFailingClient{err: err}
}

// The 8 methods below satisfy platforms.Client. They all return the
// stored error so a misconfigured Client (e.g. an invalid base URL
// caught at construction time) surfaces a typed *platforms.Error
// through the normal bundler / retry / exit-code path instead of
// panicking. Tests that need per-method behaviour (call counters,
// recorders, etc.) embed *AlwaysFailingClient and override the
// method they care about; see TestAlwaysFailingClient_EmbeddableForCounting.

// GetProject returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) GetProject(_ context.Context, _ string) (*Project, error) {
	return nil, a.err
}

// GetBranch returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) GetBranch(_ context.Context, _, _ string) (bool, error) {
	return false, a.err
}

// CreateBranch returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) CreateBranch(_ context.Context, _, _, _ string) error {
	return a.err
}

// GetFile returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) GetFile(_ context.Context, _, _, _ string) (*File, error) {
	return nil, a.err
}

// CreateFile returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) CreateFile(_ context.Context, _, _, _, _, _ string, _ io.Reader) error {
	return a.err
}

// UpdateFile returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) UpdateFile(_ context.Context, _, _, _, _, _, _ string, _ io.Reader) error {
	return a.err
}

// ListOpenMR returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) ListOpenMR(_ context.Context, _, _, _ string) (*MergeRequest, error) {
	return nil, a.err
}

// CreateMR returns the stored error. See AlwaysFailingClient.
func (a *AlwaysFailingClient) CreateMR(_ context.Context, _ string, _ CreateMRInput) (*MergeRequest, error) {
	return nil, a.err
}
