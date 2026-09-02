package platforms

import (
	"context"
	"io"
)

// Project is the subset of gitlab.Project needed by the bundler.
type Project struct {
	ID            int
	DefaultBranch string
	WebURL        string
}

// File describes a file in a GitLab repository along with the last commit
// that touched it. Content is the raw (base64-decoded) bytes.
type File struct {
	Path         string
	Content      []byte
	LastCommitID string
}

// MergeRequest describes a GitLab merge request.
type MergeRequest struct {
	IID          int
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
	WebURL       string
}

// CreateMRInput is the input to CreateMR.
type CreateMRInput struct {
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
}

// Client is the interface the bundler uses to talk to GitLab. Implementations
// include OfficialClient (production) and recordingClient (--dry-run and tests).
type Client interface {
	GetProject(ctx context.Context, repoPath string) (*Project, error)
	GetBranch(ctx context.Context, repoPath, branch string) (exists bool, err error)
	GetFile(ctx context.Context, repoPath, filePath, ref string) (*File, error)
	CreateFile(ctx context.Context, repoPath, branch, filePath, commitMsg string, content io.Reader) error
	UpdateFile(ctx context.Context, repoPath, branch, filePath, commitMsg, lastCommitID string, content io.Reader) error
	ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*MergeRequest, error)
	CreateMR(ctx context.Context, repoPath string, in CreateMRInput) (*MergeRequest, error)
}
