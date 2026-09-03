package gitlab

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/inful/repo-mr-file/internal/platforms"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// NewOfficialClient constructs a platforms.Client that talks to a real GitLab
// instance via the official client. baseURL is the API root
// (e.g. https://gitlab.example.com/api/v4); token is the bearer token.
//
// The underlying client's built-in retry logic is disabled so that
// WithRetry can wrap the client with our own policy.
func NewOfficialClient(baseURL, token string) platforms.Client {
	client, err := gitlab.NewClient(token,
		gitlab.WithBaseURL(baseURL),
		gitlab.WithoutRetries(),
	)
	if err != nil {
		// NewClient only fails if the URL is malformed; surface as a
		// persistent error wrapped in a Client that always returns it.
		return platforms.NewAlwaysFailingClient(platforms.New(platforms.KindConfig, "NewOfficialClient", err))
	}
	return &officialClient{client: client}
}

type officialClient struct {
	client *gitlab.Client
}

func (c *officialClient) GetProject(ctx context.Context, repoPath string) (*platforms.Project, error) {
	p, _, err := c.client.Projects.GetProject(repoPath, nil, gitlab.WithContext(ctx))
	if err != nil {
		return nil, classifyError("GetProject", err)
	}
	return &platforms.Project{
		ID:            int(p.ID),
		DefaultBranch: p.DefaultBranch,
		WebURL:        p.WebURL,
	}, nil
}

func (c *officialClient) GetBranch(ctx context.Context, repoPath, branch string) (bool, error) {
	_, _, err := c.client.Branches.GetBranch(repoPath, branch, gitlab.WithContext(ctx))
	if err == nil {
		return true, nil
	}
	e := classifyError("GetBranch", err)
	if e := platforms.As(e); e != nil && e.Kind == platforms.KindNotFound {
		return false, nil
	}
	return false, e
}

func (c *officialClient) GetFile(ctx context.Context, repoPath, filePath, ref string) (*platforms.File, error) {
	f, _, err := c.client.RepositoryFiles.GetFile(repoPath, filePath,
		&gitlab.GetFileOptions{Ref: &ref},
		gitlab.WithContext(ctx))
	if err != nil {
		return nil, classifyError("GetFile", err)
	}
	content, decErr := base64.StdEncoding.DecodeString(f.Content)
	if decErr != nil {
		return nil, platforms.New(platforms.KindInternal, "GetFile", fmt.Errorf("decode base64: %w", decErr))
	}
	return &platforms.File{
		Path:         f.FilePath,
		Content:      content,
		LastCommitID: f.LastCommitID,
	}, nil
}

func (c *officialClient) CreateFile(ctx context.Context, repoPath, branch, filePath, commitMsg string, content io.Reader) error {
	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return platforms.New(platforms.KindConfig, "CreateFile", err)
	}
	encoded := base64.StdEncoding.EncodeToString(contentBytes)
	encoding := "base64"
	opts := &gitlab.CreateFileOptions{
		Branch:        &branch,
		Encoding:      &encoding,
		Content:       &encoded,
		CommitMessage: &commitMsg,
	}
	// startBranch intentionally omitted from this signature: the
	// bundler calls CreateBranch before CreateFile (when needed),
	// so the branch always exists by this point. Passing
	// start_branch to GitLab would make Files::CreateService
	// invoke Branches::CreateService a second time and fail with
	// "A branch called 'X' already exists" (HTTP 400). The error
	// happens even after our own CreateBranch has just succeeded,
	// because GitLab's branch-existence check is part of the
	// start_branch handling path, not a separate "exists → skip"
	// guard.
	_, _, err = c.client.RepositoryFiles.CreateFile(repoPath, filePath, opts,
		gitlab.WithContext(ctx))
	if err != nil {
		return classifyError("CreateFile", err)
	}
	return nil
}

func (c *officialClient) UpdateFile(ctx context.Context, repoPath, branch, filePath, _, commitMsg, lastCommitID string, content io.Reader) error {
	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return platforms.New(platforms.KindConfig, "UpdateFile", err)
	}
	encoded := base64.StdEncoding.EncodeToString(contentBytes)
	encoding := "base64"
	_, _, err = c.client.RepositoryFiles.UpdateFile(repoPath, filePath,
		&gitlab.UpdateFileOptions{
			Branch:        &branch,
			Encoding:      &encoding,
			Content:       &encoded,
			CommitMessage: &commitMsg,
			LastCommitID:  &lastCommitID,
		},
		gitlab.WithContext(ctx))
	if err != nil {
		return classifyError("UpdateFile", err)
	}
	return nil
}

// CreateBranch creates a new ref via POST /projects/:id/repository/branches
// using the official client's Branches service. Implements the
// platforms.Client.CreateBranch contract — called by the bundler
// before any file POST to ensure the target branch exists. The
// `startBranch` parameter is the parent ref name (e.g. "main").
//
// GitLab's API idempotency: when the branch already exists, the
// response is 400 with a body containing "has already been taken"
// (the Rails-style validation message on the `name` field). We
// treat that as success because the workflow's goal (ensure the
// branch exists) is satisfied. Other 400 responses (malformed
// branch name, missing ref, etc.) are real errors and must NOT
// be silently swallowed — that's why the body check matters
// here, and why the prior "any 400 → success" code path was a bug.
func (c *officialClient) CreateBranch(ctx context.Context, repoPath, newBranch, startBranch string) error {
	_, _, err := c.client.Branches.CreateBranch(repoPath,
		&gitlab.CreateBranchOptions{
			Branch: &newBranch,
			Ref:    &startBranch,
		},
		gitlab.WithContext(ctx))
	if err != nil {
		// "has already been taken" on a 400 means the branch
		// exists. Treat as success.
		var er *gitlab.ErrorResponse
		if errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusBadRequest {
			if bytes.Contains(er.Body, []byte("has already been taken")) {
				return nil
			}
		}
		return classifyError("CreateBranch", err)
	}
	return nil
}

func (c *officialClient) ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*platforms.MergeRequest, error) {
	state := "opened"
	mrs, _, err := c.client.MergeRequests.ListProjectMergeRequests(repoPath,
		&gitlab.ListProjectMergeRequestsOptions{
			SourceBranch: &sourceBranch,
			TargetBranch: &targetBranch,
			State:        &state,
		},
		gitlab.WithContext(ctx))
	if err != nil {
		return nil, classifyError("ListOpenMR", err)
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	first := mrs[0]
	return &platforms.MergeRequest{
		IID:          int(first.IID),
		SourceBranch: first.SourceBranch,
		TargetBranch: first.TargetBranch,
		Title:        first.Title,
		Description:  first.Description,
		WebURL:       first.WebURL,
	}, nil
}

func (c *officialClient) CreateMR(ctx context.Context, repoPath string, in platforms.CreateMRInput) (*platforms.MergeRequest, error) {
	mr, _, err := c.client.MergeRequests.CreateMergeRequest(repoPath,
		&gitlab.CreateMergeRequestOptions{
			SourceBranch: &in.SourceBranch,
			TargetBranch: &in.TargetBranch,
			Title:        &in.Title,
			Description:  &in.Description,
		},
		gitlab.WithContext(ctx))
	if err != nil {
		return nil, classifyError("CreateMR", err)
	}
	return &platforms.MergeRequest{
		IID:          int(mr.IID),
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
		Title:        mr.Title,
		Description:  mr.Description,
		WebURL:       mr.WebURL,
	}, nil
}

// classifyError maps an error from the official client into a typed
// *platforms.Error carrying the appropriate Kind. Special handling for
// ErrNotFound (the official client returns this for 404s without an
// embedded response) and for net.Error (any transient network failure).
//
// When the error wraps a *gitlab.ErrorResponse, the response header is
// consulted for Retry-After and that value (capped at
// platforms.MaxRetryAfter) is carried on the typed error so retryDo
// waits the server-suggested interval instead of the configured
// exponential backoff.
func classifyError(op string, err error) error {
	if err == nil {
		return nil
	}
	// The official client returns ErrNotFound directly when 404 is hit and
	// the per-method code path doesn't construct an ErrorResponse.
	if errors.Is(err, gitlab.ErrNotFound) {
		return platforms.New(platforms.KindNotFound, op, err)
	}
	var er *gitlab.ErrorResponse
	if errors.As(err, &er) {
		status := er.Response.StatusCode
		kind := platforms.ClassifyStatus(status)
		out := &platforms.Error{Kind: kind, Op: op, Err: err, StatusCode: status}
		if er.Response != nil {
			out.RetryAfter = platforms.RetryAfterFromHeader(er.Response.Header)
		}
		// GitLab response bodies don't carry a structured token-state
		// signal, so we fall back to the generic 401-vs-403 hint.
		if kind == platforms.KindAuth {
			out.Hint = platforms.AuthHint("GitLab", status)
		}
		return out
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return &platforms.Error{Kind: platforms.KindTransient, Op: op, Err: err}
	}
	return &platforms.Error{Kind: platforms.KindUnknown, Op: op, Err: err}
}
