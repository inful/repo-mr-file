package platforms

import (
	"context"
	"io"
)

// Project is the subset of the platform project response needed by the bundler.
type Project struct {
	ID            int
	DefaultBranch string
	WebURL        string
}

// File describes a file in a repository along with the last commit that
// touched it. Content is the raw (base64-decoded) bytes.
type File struct {
	Path         string
	Content      []byte
	LastCommitID string
}

// MergeRequest describes an open merge request (GitLab) or pull request (GitHub).
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

// Client is the interface the bundler uses to talk to any supported platform
// (GitLab, GitHub, Gitea, Forgejo). Platform-specific implementations are
// in sub-packages; this package provides retry, recording, and dry-run wrappers.
//
// CreateBranch is invoked by the bundler when GetBranch returns
// false, before any file POST. By the time CreateFile is called, the
// branch is guaranteed to exist (either it existed already or the
// bundler just created it), so CreateFile takes only the target
// branch name — no startBranch parameter. Earlier versions passed
// startBranch so platforms like GitLab could auto-create the branch
// on POST /repository/files, but that path was buggy: GitLab
// unconditionally tried to re-create the branch when start_branch
// was set, returning HTTP 400 "A branch called 'X' already exists"
// even after CreateBranch had just succeeded.
//
// UpdateFile still takes startBranch for historical signature
// stability; no platform implementation uses it (GitHub requires
// the branch to exist — ensured by CreateBranch; GitLab and
// Gitea/Forgejo PUT /repository/files doesn't auto-create either).
type Client interface {
	GetProject(ctx context.Context, repoPath string) (*Project, error)
	GetBranch(ctx context.Context, repoPath, branch string) (exists bool, err error)
	CreateBranch(ctx context.Context, repoPath, newBranch, startBranch string) error
	GetFile(ctx context.Context, repoPath, filePath, ref string) (*File, error)
	CreateFile(ctx context.Context, repoPath, branch, filePath, commitMsg string, content io.Reader) error
	UpdateFile(ctx context.Context, repoPath, branch, filePath, startBranch, commitMsg, lastCommitID string, content io.Reader) error
	ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*MergeRequest, error)
	CreateMR(ctx context.Context, repoPath string, in CreateMRInput) (*MergeRequest, error)
}
