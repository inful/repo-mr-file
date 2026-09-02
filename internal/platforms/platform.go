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
type Client interface {
	GetProject(ctx context.Context, repoPath string) (*Project, error)
	GetBranch(ctx context.Context, repoPath, branch string) (exists bool, err error)
	GetFile(ctx context.Context, repoPath, filePath, ref string) (*File, error)
	CreateFile(ctx context.Context, repoPath, branch, filePath, commitMsg string, content io.Reader) error
	UpdateFile(ctx context.Context, repoPath, branch, filePath, commitMsg, lastCommitID string, content io.Reader) error
	ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*MergeRequest, error)
	CreateMR(ctx context.Context, repoPath string, in CreateMRInput) (*MergeRequest, error)
}
