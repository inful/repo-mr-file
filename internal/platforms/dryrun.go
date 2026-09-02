package platforms

import (
	"context"
	"errors"
	"io"
)

// dryRunClient is a Client that records every call (via an embedded
// recordingClient) and returns values that let the bundler complete its
// workflow without any real network calls. Used for --dry-run mode.
type dryRunClient struct {
	*recordingClient
}

// NewDryRunClient returns a Client that records calls and returns synthetic
// responses suitable for the bundler's dry-run path. See internal/bundler
// for the workflow that uses it.
func NewDryRunClient() Client {
	return &dryRunClient{recordingClient: &recordingClient{}}
}

func (d *dryRunClient) GetProject(_ context.Context, repoPath string) (*Project, error) {
	d.record("GetProject", repoPath)
	return &Project{ID: 0, DefaultBranch: "main"}, nil
}

func (d *dryRunClient) GetBranch(_ context.Context, repoPath, branch string) (bool, error) {
	d.record("GetBranch", repoPath, branch)
	return false, nil
}

func (d *dryRunClient) GetFile(_ context.Context, repoPath, filePath, ref string) (*File, error) {
	d.record("GetFile", repoPath, filePath, ref)
	// Return a synthetic KindNotFound so the bundler treats the file as
	// missing and chooses the POST path.
	return nil, New(KindNotFound, "GetFile(dry-run)", errors.New("dry-run: file not fetched"))
}

func (d *dryRunClient) CreateFile(_ context.Context, repoPath, branch, filePath, commitMsg string, _ io.Reader) error {
	d.record("CreateFile", repoPath, branch, filePath, commitMsg)
	return nil
}

func (d *dryRunClient) UpdateFile(_ context.Context, repoPath, branch, filePath, commitMsg, lastCommitID string, _ io.Reader) error {
	d.record("UpdateFile", repoPath, branch, filePath, commitMsg, lastCommitID)
	return nil
}

func (d *dryRunClient) ListOpenMR(_ context.Context, repoPath, sourceBranch, targetBranch string) (*MergeRequest, error) {
	d.record("ListOpenMR", repoPath, sourceBranch, targetBranch)
	return nil, nil
}

func (d *dryRunClient) CreateMR(_ context.Context, repoPath string, in CreateMRInput) (*MergeRequest, error) {
	d.record("CreateMR", repoPath, in)
	return &MergeRequest{IID: 0, WebURL: "https://example.invalid/dry-run"}, nil
}
