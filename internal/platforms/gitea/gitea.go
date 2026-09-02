// Package gitea implements platforms.Client for Gitea and Forgejo (which
// is a hard fork of Gitea and inherits its API surface — the user-facing
// flag is `--platform=gitea` or `--platform=forgejo` and the same Client
// backs both).
//
// Auth header: "Authorization: token <token>" (note the literal word
// "token", not "Bearer" — that's how Gitea distinguishes personal access
// tokens from OAuth bearer tokens).
//
// Stale-branch-detection field name: "sha" (the file's blob SHA), which
// is also exposed as "last_commit_sha" in contents responses. PUT /contents
// requires sha; a mismatch returns 422 (not 409 like GitLab).
//
// Branch creation is explicit: CreateFile must call CreateBranch first if
// the target branch doesn't exist yet. Gitea doesn't auto-create the
// branch on POST /contents the way GitLab and GitHub do.
package gitea

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/inful/repo-mr-file/internal/platforms"
)

// NewOfficialClient constructs a platforms.Client that talks to a real Gitea
// (or Forgejo — they're API-compatible) instance. baseURL is the API
// root (e.g. https://gitea.example.com/api/v1); token is a personal
// access token with repo scope.
func NewOfficialClient(baseURL, token string) platforms.Client {
	return &officialClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{},
	}
}

type officialClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// do executes a request and returns the response body bytes on success
// (status 200/201/204) or nil with a typed *platforms.Error otherwise.
func (c *officialClient) do(ctx context.Context, method, path string, body, v any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return platforms.New(platforms.KindConfig, method+" "+path, fmt.Errorf("marshal: %w", err))
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return platforms.New(platforms.KindConfig, method+" "+path, fmt.Errorf("new request: %w", err))
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &platforms.Error{Kind: platforms.KindTransient, Op: method + " " + path, Err: err}
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if v == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return platforms.New(platforms.KindInternal, method+" "+path, fmt.Errorf("decode: %w", err))
		}
		return nil
	}

	// Non-2xx: classify by status.
	respBody, _ := io.ReadAll(resp.Body)
	return &platforms.Error{
		Kind:       platforms.ClassifyStatus(resp.StatusCode),
		Op:         method + " " + path,
		Err:        fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody)),
		StatusCode: resp.StatusCode,
		RetryAfter: platforms.RetryAfterFromHeader(resp.Header),
	}
}

func (c *officialClient) GetProject(ctx context.Context, repoPath string) (*platforms.Project, error) {
	var r struct {
		ID            int    `json:"id"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+repoPath, nil, &r); err != nil {
		return nil, err
	}
	return &platforms.Project{ID: r.ID, DefaultBranch: r.DefaultBranch}, nil
}

func (c *officialClient) GetBranch(ctx context.Context, repoPath, branch string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/api/v1/repos/"+repoPath+"/branches/"+url.PathEscape(branch), nil, nil)
	if err == nil {
		return true, nil
	}
	if e := platforms.As(err); e != nil && e.Kind == platforms.KindNotFound {
		return false, nil
	}
	return false, err
}

func (c *officialClient) GetFile(ctx context.Context, repoPath, filePath, ref string) (*platforms.File, error) {
	path := "/api/v1/repos/" + repoPath + "/contents/" + filePath + "?ref=" + url.QueryEscape(ref)
	var r struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return nil, err
	}
	if r.Encoding != "base64" {
		return nil, platforms.New(platforms.KindInternal, "GetFile", fmt.Errorf("unexpected content encoding %q", r.Encoding))
	}
	content, err := base64.StdEncoding.DecodeString(r.Content)
	if err != nil {
		return nil, platforms.New(platforms.KindInternal, "GetFile", fmt.Errorf("decode base64: %w", err))
	}
	return &platforms.File{Path: filePath, Content: content, LastCommitID: r.SHA}, nil
}

// CreateBranch creates a new branch from an existing one. Returns nil on
// 201; on 409 (branch already exists) the function treats the response as
// success — idempotent.
func (c *officialClient) CreateBranch(ctx context.Context, repoPath, newBranch, oldBranch string) error {
	body := map[string]string{
		"new_branch_name": newBranch,
		"old_branch_name": oldBranch,
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/repos/"+repoPath+"/branches", body, nil)
	if err == nil {
		return nil
	}
	if e := platforms.As(err); e != nil && e.StatusCode == http.StatusConflict {
		// Branch already exists — idempotent.
		return nil
	}
	return err
}

func (c *officialClient) CreateFile(ctx context.Context, repoPath, branch, filePath, commitMsg string, content io.Reader) error {
	// Gitea doesn't auto-create the branch on POST /contents. If the
	// branch doesn't exist, create it from the target branch first.
	exists, err := c.GetBranch(ctx, repoPath, branch)
	if err != nil {
		return err
	}
	if !exists {
		// Best-effort: derive the source branch as the target branch by
		// calling GetProject. If we can't resolve it, fall back to the
		// default branch; if even that fails, return the error.
		targetBranch := branch
		proj, perr := c.GetProject(ctx, repoPath)
		if perr == nil && proj.DefaultBranch != "" {
			targetBranch = proj.DefaultBranch
		}
		if err := c.CreateBranch(ctx, repoPath, branch, targetBranch); err != nil {
			return err
		}
	}

	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return platforms.New(platforms.KindConfig, "CreateFile", err)
	}
	encoded := base64.StdEncoding.EncodeToString(contentBytes)
	body := map[string]string{
		"branch":  branch,
		"content": encoded,
		"message": commitMsg,
	}
	return c.do(ctx, http.MethodPost, "/api/v1/repos/"+repoPath+"/contents/"+filePath, body, nil)
}

func (c *officialClient) UpdateFile(ctx context.Context, repoPath, branch, filePath, commitMsg, lastCommitID string, content io.Reader) error {
	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return platforms.New(platforms.KindConfig, "UpdateFile", err)
	}
	encoded := base64.StdEncoding.EncodeToString(contentBytes)
	body := map[string]string{
		"branch":  branch,
		"content": encoded,
		"message": commitMsg,
		"sha":     lastCommitID, // required for update
	}
	return c.do(ctx, http.MethodPut, "/api/v1/repos/"+repoPath+"/contents/"+filePath, body, nil)
}

func (c *officialClient) ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*platforms.MergeRequest, error) {
	// Gitea has a dedicated lookup-by-base-head endpoint. 404 → no match.
	path := "/api/v1/repos/" + repoPath + "/pulls/" + url.PathEscape(targetBranch) + "/" + url.PathEscape(sourceBranch)
	var prs []struct {
		ID      int    `json:"id"`
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &prs)
	if err != nil {
		if e := platforms.As(err); e != nil && e.Kind == platforms.KindNotFound {
			return nil, nil
		}
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	first := prs[0]
	return &platforms.MergeRequest{
		IID:          first.Number,
		SourceBranch: sourceBranch,
		TargetBranch: targetBranch,
		WebURL:       first.HTMLURL,
	}, nil
}

func (c *officialClient) CreateMR(ctx context.Context, repoPath string, in platforms.CreateMRInput) (*platforms.MergeRequest, error) {
	body := map[string]string{
		"head":  in.SourceBranch,
		"base":  in.TargetBranch,
		"title": in.Title,
		"body":  in.Description,
	}
	var r struct {
		ID      int    `json:"id"`
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/repos/"+repoPath+"/pulls", body, &r); err != nil {
		return nil, err
	}
	return &platforms.MergeRequest{
		IID:          r.Number,
		SourceBranch: in.SourceBranch,
		TargetBranch: in.TargetBranch,
		Title:        in.Title,
		Description:  in.Description,
		WebURL:       r.HTMLURL,
	}, nil
}
