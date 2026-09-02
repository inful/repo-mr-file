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
// Branch creation: the bundler calls CreateBranch via the
// platforms.Client interface before any file POST. GitHub's PUT
// /contents auto-creates branches on *empty* repos, but returns
// "404 Branch not found" on populated repos, so the explicit
// CreateRef is required for the populated-repo case (which the
// README oversimplified as "implicit via branch on PUT file").
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
// built via gh.NewClient(...)). username is the GitHub handle that
// owns the token; it's used to format PR "head" fields as
// "username:branch" (the format GitHub requires). baseURL overrides the
// go-github default of https://api.github.com (useful for GitHub
// Enterprise Server); pass "" to use the default.
//
// Returns platforms.Client (not the concrete *Client) for symmetry
// with gitlab.NewOfficialClient and gitea.NewOfficialClient. The
// concrete *Client type is unexported; callers who need to access
// fields directly (e.g. for tests) live in the same package.
func NewClient(ghClient *gh.Client, username, token, baseURL string) (platforms.Client, error) {
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
	return &client{gh: ghClient, user: username}, nil
}

// client implements platforms.Client for GitHub. Unexported: callers
// reach the implementation only via NewClient, which returns the
// platforms.Client interface. This matches gitlab.officialClient and
// gitea.officialClient.
type client struct {
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

// branchExists reports whether the named ref exists in the repo. It
// uses Repositories.GetBranch (HEAD: refs/heads/<branch>) and treats
// 404 as "does not exist". The second return is the SHA when the
// branch exists, "" otherwise.
//
// IMPORTANT: go-github v74's Repositories.GetBranch returns a
// generic fmt.Errorf on non-200 (not a typed *ErrorResponse), so the
// status code is read off the *Response header rather than parsed
// from the error. Other API methods (CreateRef, etc.) DO return
// *ErrorResponse and are handled by ghError.
func (c *client) branchExists(ctx context.Context, owner, repo, branch string) (bool, string, error) {
	b, resp, err := c.gh.Repositories.GetBranch(ctx, owner, repo, branch, 0)
	if err == nil {
		return true, b.GetCommit().GetSHA(), nil
	}
	if resp != nil && resp.StatusCode == http.StatusNotFound {
		return false, "", nil
	}
	return false, "", classifyErr("CreateBranch", err)
}

// ghError extracts the HTTP status and headers from a *gh.ErrorResponse
// so we can classify it into a typed *platforms.Error. Returns (status,
// header, true) if err is (or wraps) a GitHub API error response; header
// is the underlying response header (used for Retry-After parsing).
func ghError(err error) (int, http.Header, bool) {
	var ghErr *gh.ErrorResponse
	if errors.As(err, &ghErr) && ghErr.Response != nil {
		return ghErr.Response.StatusCode, ghErr.Response.Header, true
	}
	return 0, nil, false
}

// classifyErr maps a Go-github error into a typed *platforms.Error.
func classifyErr(op string, err error) error {
	if status, hdr, ok := ghError(err); ok {
		out := &platforms.Error{
			Kind:       platforms.ClassifyStatus(status),
			Op:         op,
			Err:        err,
			StatusCode: status,
		}
		if hdr != nil {
			out.RetryAfter = platforms.RetryAfterFromHeader(hdr)
		}
		return out
	}
	// Network or unexpected error → transient so the bundler retries.
	return &platforms.Error{Kind: platforms.KindTransient, Op: op, Err: err}
}

// CreateBranch creates refs/heads/<newBranch> at startBranch's SHA
// via POST /git/refs. Implements the platforms.Client.CreateBranch
// contract — the bundler invokes this before any file POST to
// guarantee the target branch exists.
//
// IMPORTANT: github.com/google/go-github/v74's Repositories.GetBranch
// returns a generic fmt.Errorf on non-200 (not a typed *ErrorResponse),
// so the status code is read off the *Response header rather than
// parsed from the error. Other API methods (CreateRef, etc.) DO
// return *ErrorResponse and are handled by ghError.
func (c *client) CreateBranch(ctx context.Context, repoPath, newBranch, startBranch string) error {
	owner, repo, err := splitRepoPath(repoPath)
	if err != nil {
		return &platforms.Error{Kind: platforms.KindConfig, Op: "CreateBranch", Err: err}
	}
	exists, _, err := c.branchExists(ctx, owner, repo, startBranch)
	if err != nil {
		return err
	}
	if !exists {
		return &platforms.Error{Kind: platforms.KindConfig, Op: "CreateBranch",
			Err: fmt.Errorf("start branch %q not found for branching %q", startBranch, newBranch)}
	}
	_, parentSHA, err := c.branchExists(ctx, owner, repo, startBranch)
	if err != nil || parentSHA == "" {
		// Should be unreachable (we just asserted existence), but
		// be defensive.
		return &platforms.Error{Kind: platforms.KindTransient, Op: "CreateBranch",
			Err: fmt.Errorf("could not resolve parent SHA for %q", startBranch)}
	}
	_, _, err = c.gh.Git.CreateRef(ctx, owner, repo, &gh.Reference{
		Ref: gh.Ptr("refs/heads/" + newBranch),
		Object: &gh.GitObject{
			SHA: gh.Ptr(parentSHA),
		},
	})
	if err != nil {
		// 422 'Reference already exists' is a successful no-op for our
		// purposes (some other process created it).
		status, _, ok := ghError(err)
		if ok && status == http.StatusUnprocessableEntity {
			return nil
		}
		return classifyErr("CreateBranch", err)
	}
	return nil
}

// GetProject returns repository metadata (id + default_branch) so the
// bundler can resolve a default target branch when --target-branch
// was not specified.
func (c *client) GetProject(ctx context.Context, repoPath string) (*platforms.Project, error) {
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
func (c *client) GetBranch(ctx context.Context, repoPath, branch string) (bool, error) {
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
	if status, _, ok := ghError(err); ok && status == http.StatusNotFound {
		return false, nil
	}
	return false, classifyErr("GetBranch", err)
}

// GetFile returns the file at filePath on branch ref, decoding the
// base64 content and capturing the blob SHA for stale-branch detection.
func (c *client) GetFile(ctx context.Context, repoPath, filePath, ref string) (*platforms.File, error) {
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
		if status, _, ok := ghError(err); ok && status == http.StatusNotFound {
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

// CreateFile creates the file at filePath on the given branch. The
// bundler is responsible for ensuring the branch exists (via
// CreateBranch) before this is called — see the platforms.Client
// interface comment for why each platform gets its own explicit
// branch-creation call rather than relying on per-platform
// auto-create quirks that only work on empty repos.
//
// `startBranch` is unused here for symmetry with the platforms.Client
// interface contract; the bundler hands it to CreateBranch at the
// right moment.
func (c *client) CreateFile(ctx context.Context, repoPath, branch, filePath, _, commitMsg string, content io.Reader) error {
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
//
// As with CreateFile, the bundler guarantees the target branch
// exists via CreateBranch beforehand — this method just does the PUT.
func (c *client) UpdateFile(ctx context.Context, repoPath, branch, filePath, _, commitMsg, lastCommitID string, content io.Reader) error {
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
func (c *client) ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*platforms.MergeRequest, error) {
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
func (c *client) CreateMR(ctx context.Context, repoPath string, in platforms.CreateMRInput) (*platforms.MergeRequest, error) {
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
