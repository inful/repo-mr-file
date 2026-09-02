// Package github implements platforms.Client for GitHub using the official
// google/go-github library.
//
// Auth header: "Authorization: Bearer <token>" (classic personal access
// tokens and GitHub App installation tokens both use Bearer).
//
// Stale-branch-detection field name: "sha" (the file blob SHA), which
// is what Contents.Get and Contents.Update return. PUT /contents requires
// sha and returns 422 on mismatch (not 409 like GitLab; the platforms
// layer maps 422 → KindConflict).
//
// Branch creation: GitHub's PUT /contents auto-creates the branch when
// the requested branch doesn't exist, so no separate CreateBranch step
// is needed (unlike Gitea/Forgejo).
//
// Pull request "head" must be in "user:branch" form. The bundler doesn't
// know the GitHub user, so the client constructor takes a username
// argument (typically the GitHub handle that owns the token).
package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v74/github"

	"github.com/inful/repo-mr-file/internal/platforms"
)

// NewClient constructs a platforms.Client that talks to a real GitHub
// instance. ghClient is the user-supplied go-github client (typically
// built via github.NewClient(...)). username is the GitHub handle that
// owns the token; it's used to format PR "head" fields as
// "username:branch" (the format GitHub requires). baseURL overrides the
// go-github default of https://api.github.com (useful for GitHub
// Enterprise Server); pass "" to use the default.
func NewClient(ghClient *gh.Client, username, token, baseURL string) (*Client, error) {
	if username == "" {
		return nil, errors.New("github: username required for PR 'head' field formatting")
	}
	if token != "" {
		ghClient = ghClient.WithAuthToken(token)
	}
	if baseURL != "" {
		// If baseURL already includes the /api/v3 path (e.g. "https://host/api/v3/"),
		// go-github's WithEnterpriseURLs appends /api/v3/ unconditionally. Strip
		// it before calling so we don't end up with /api/v3/api/v3/.
		base := baseURL
		base = strings.TrimSuffix(base, "/")
		base = strings.TrimSuffix(base, "/api/v3")
		var err error
		ghClient, err = ghClient.WithEnterpriseURLs(base, base)
		if err != nil {
			return nil, fmt.Errorf("github: invalid base URL %q: %w", baseURL, err)
		}
	}
	return &Client{gh: ghClient, user: username}, nil
}

// Client implements platforms.Client for GitHub.
type Client struct {
	gh   *gh.Client
	user string // GitHub handle used to format PR head fields
}

// splitRepoPath splits "owner/repo" or "group/subgroup/repo" into owner + repo.
// Returns an error if the path has no slash.
func splitRepoPath(repoPath string) (owner, repo string, err error) {
	parts := strings.SplitN(repoPath, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo path %q (expected owner/repo)", repoPath)
	}
	return parts[0], parts[1], nil
}

// ghError extracts the HTTP status from a *gh.ErrorResponse so we can
// classify it into a typed *platforms.Error. Returns (status, true) if
// err is (or wraps) a GitHub API error response.
func ghError(err error) (int, bool) {
	var ghErr *gh.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		return ghErr.Response.StatusCode, true
	}
	return 0, false
}

// classifyErr maps a Go-github error into a typed *platforms.Error.
func classifyErr(op string, err error) error {
	if status, ok := ghError(err); ok {
		return &platforms.Error{
			Kind:       platforms.ClassifyStatus(status),
			Op:         op,
			Err:        err,
			StatusCode: status,
		}
	}
	// Network or unexpected error → transient so the bundler retries.
	return &platforms.Error{Kind: platforms.KindTransient, Op: op, Err: err}
}

// GetProject returns the repository metadata for repoPath (formatted as
// "owner/repo"). It satisfies platforms.Client.
func (c *Client) GetProject(ctx context.Context, repoPath string) (*platforms.Project, error) {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return nil, &platforms.Error{Kind: platforms.KindConfig, Op: "GetProject", Err: err}
	}
	r, _, err := c.gh.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return nil, classifyErr("GetProject", err)
	}
	return &platforms.Project{
		ID:            int(r.GetID()),
		DefaultBranch: r.GetDefaultBranch(),
		WebURL:        r.GetHTMLURL(),
	}, nil
}

// GetBranch returns whether branch exists in the repo. Returns (false, nil)
// for a 404, matching the bundler's skip-the-write semantics.
func (c *Client) GetBranch(ctx context.Context, repoPath, branch string) (bool, error) {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return false, &platforms.Error{Kind: platforms.KindConfig, Op: "GetBranch", Err: err}
	}
	_, resp, err := c.gh.Repositories.GetBranch(ctx, owner, repo, branch, 0)
	if err == nil {
		return true, nil
	}
	// go-github returns a generic fmt.Errorf for non-200 responses (not
	// an *ErrorResponse), so we have to read the status from the response
	// directly to recognize 404.
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if status, ok := ghError(err); ok && status == http.StatusNotFound {
		return false, nil
	}
	return false, classifyErr("GetBranch", err)
}

// GetFile returns the file at filePath on branch ref, decoding the
// base64 content and capturing the blob SHA for stale-branch detection.
func (c *Client) GetFile(ctx context.Context, repoPath, filePath, ref string) (*platforms.File, error) {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return nil, &platforms.Error{Kind: platforms.KindConfig, Op: "GetFile", Err: err}
	}
	opts := &gh.RepositoryContentGetOptions{Ref: ref}
	fc, _, resp, err := c.gh.Repositories.GetContents(ctx, owner, repo, filePath, opts)
	if err != nil {
		// go-github returns fmt.Errorf for 404, not *ErrorResponse, so
		// check resp.StatusCode first.
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, &platforms.Error{Kind: platforms.KindNotFound, Op: "GetFile", Err: err, StatusCode: http.StatusNotFound}
		}
		if status, ok := ghError(err); ok && status == http.StatusNotFound {
			return nil, &platforms.Error{Kind: platforms.KindNotFound, Op: "GetFile", Err: err, StatusCode: status}
		}
		return nil, classifyErr("GetFile", err)
	}
	content, err := fc.GetContent()
	if err != nil {
		return nil, &platforms.Error{Kind: platforms.KindInternal, Op: "GetFile", Err: fmt.Errorf("decode base64: %w", err)}
	}
	return &platforms.File{
		Path:         fc.GetPath(),
		Content:      []byte(content),
		LastCommitID: fc.GetSHA(),
	}, nil
}

// CreateFile creates the file at filePath on the given branch, creating
// the branch implicitly if it doesn't exist. GitHub's PUT /contents
// auto-creates branches, so no separate CreateBranch call is needed.
func (c *Client) CreateFile(ctx context.Context, repoPath, branch, filePath, commitMsg string, content io.Reader) error {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return &platforms.Error{Kind: platforms.KindConfig, Op: "CreateFile", Err: err}
	}
	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return &platforms.Error{Kind: platforms.KindConfig, Op: "CreateFile", Err: fmt.Errorf("read content: %w", err)}
	}
	opts := &gh.RepositoryContentFileOptions{
		Content: contentBytes,
		Branch:  gh.Ptr(branch),
		Message: gh.Ptr(commitMsg),
	}
	_, _, err = c.gh.Repositories.CreateFile(ctx, owner, repo, filePath, opts)
	if err != nil {
		return classifyErr("CreateFile", err)
	}
	return nil
}

// UpdateFile updates the file at filePath on branch. lastCommitID is
// the blob SHA returned by a prior GetFile; if it doesn't match,
// GitHub returns 422 (mapped to KindConflict at the platforms layer).
func (c *Client) UpdateFile(ctx context.Context, repoPath, branch, filePath, commitMsg, lastCommitID string, content io.Reader) error {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return &platforms.Error{Kind: platforms.KindConfig, Op: "UpdateFile", Err: err}
	}
	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return &platforms.Error{Kind: platforms.KindConfig, Op: "UpdateFile", Err: fmt.Errorf("read content: %w", err)}
	}
	opts := &gh.RepositoryContentFileOptions{
		Content: contentBytes,
		Branch:  gh.Ptr(branch),
		Message: gh.Ptr(commitMsg),
		SHA:     gh.Ptr(lastCommitID),
	}
	_, _, err = c.gh.Repositories.UpdateFile(ctx, owner, repo, filePath, opts)
	if err != nil {
		return classifyErr("UpdateFile", err)
	}
	return nil
}

// ListOpenMR returns an open MR from sourceBranch into targetBranch, or
// nil if no such MR exists. GitHub requires head to be "user:branch";
// we format it from the c.user field captured at construction.
func (c *Client) ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*platforms.MergeRequest, error) {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return nil, &platforms.Error{Kind: platforms.KindConfig, Op: "ListOpenMR", Err: err}
	}
	// GitHub requires the head field to be "user:branch".
	head := c.user + ":" + sourceBranch
	opts := &gh.PullRequestListOptions{
		State: "open",
		Head:  head,
		Base:  targetBranch,
	}
	prs, _, err := c.gh.PullRequests.List(ctx, owner, repo, opts)
	if err != nil {
		return nil, classifyErr("ListOpenMR", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	pr := prs[0]
	return &platforms.MergeRequest{
		IID:          pr.GetNumber(),
		SourceBranch: sourceBranch,
		TargetBranch: targetBranch,
		Title:        pr.GetTitle(),
		Description:  pr.GetBody(),
		WebURL:       pr.GetHTMLURL(),
	}, nil
}

// CreateMR opens a new pull request from sourceBranch into targetBranch.
// A 422 response (concurrent MR was created) is propagated as a
// KindConflict; the bundler handles re-list-and-reuse.
func (c *Client) CreateMR(ctx context.Context, repoPath string, in platforms.CreateMRInput) (*platforms.MergeRequest, error) {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return nil, &platforms.Error{Kind: platforms.KindConfig, Op: "CreateMR", Err: err}
	}
	req := &gh.NewPullRequest{
		Title: gh.Ptr(in.Title),
		Body:  gh.Ptr(in.Description),
		Head:  gh.Ptr(c.user + ":" + in.SourceBranch),
		Base:  gh.Ptr(in.TargetBranch),
	}
	pr, _, err := c.gh.PullRequests.Create(ctx, owner, repo, req)
	if err != nil {
		return nil, classifyErr("CreateMR", err)
	}
	return &platforms.MergeRequest{
		IID:          pr.GetNumber(),
		SourceBranch: in.SourceBranch,
		TargetBranch: in.TargetBranch,
		Title:        in.Title,
		Description:  in.Description,
		WebURL:       pr.GetHTMLURL(),
	}, nil
}
