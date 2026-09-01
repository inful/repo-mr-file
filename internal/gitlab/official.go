package gitlab

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// NewOfficialClient constructs a Client that talks to a real GitLab
// instance via the official client. baseURL is the API root
// (e.g. https://gitlab.example.com/api/v4); token is the bearer token.
//
// The underlying client's built-in retry logic is disabled so that
// WithRetry can wrap the client with our own policy.
func NewOfficialClient(baseURL, token string) Client {
	client, err := gitlab.NewClient(token,
		gitlab.WithBaseURL(baseURL),
		gitlab.WithoutRetries(),
	)
	if err != nil {
		// NewClient only fails if the URL is malformed; surface as a
		// persistent error wrapped in a Client that always returns it.
		return &errClient{err: New(KindConfig, "NewOfficialClient", err)}
	}
	return &officialClient{client: client}
}

// errClient is a Client that returns a fixed error from every method. It
// is used when the underlying official client fails to construct so that
// the caller still sees a well-typed error.
type errClient struct {
	err error
}

func (e *errClient) GetProject(_ context.Context, _ string) (*Project, error) {
	return nil, e.err
}
func (e *errClient) GetBranch(_ context.Context, _, _ string) (bool, error) {
	return false, e.err
}
func (e *errClient) GetFile(_ context.Context, _, _, _ string) (*File, error) {
	return nil, e.err
}
func (e *errClient) CreateFile(_ context.Context, _, _, _, _ string, _ io.Reader) error {
	return e.err
}
func (e *errClient) UpdateFile(_ context.Context, _, _, _, _, _ string, _ io.Reader) error {
	return e.err
}
func (e *errClient) ListOpenMR(_ context.Context, _, _, _ string) (*MergeRequest, error) {
	return nil, e.err
}
func (e *errClient) CreateMR(_ context.Context, _ string, _ CreateMRInput) (*MergeRequest, error) {
	return nil, e.err
}

type officialClient struct {
	client *gitlab.Client
}

func (c *officialClient) GetProject(ctx context.Context, repoPath string) (*Project, error) {
	p, _, err := c.client.Projects.GetProject(repoPath, nil, gitlab.WithContext(ctx))
	if err != nil {
		return nil, classifyError("GetProject", err)
	}
	return &Project{
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
	if e := As(e); e != nil && e.Kind == KindNotFound {
		return false, nil
	}
	return false, e
}

func (c *officialClient) GetFile(ctx context.Context, repoPath, filePath, ref string) (*File, error) {
	f, _, err := c.client.RepositoryFiles.GetFile(repoPath, filePath,
		&gitlab.GetFileOptions{Ref: &ref},
		gitlab.WithContext(ctx))
	if err != nil {
		return nil, classifyError("GetFile", err)
	}
	content, decErr := base64.StdEncoding.DecodeString(f.Content)
	if decErr != nil {
		return nil, New(KindInternal, "GetFile", fmt.Errorf("decode base64: %w", decErr))
	}
	return &File{
		Path:         f.FilePath,
		Content:      content,
		LastCommitID: f.LastCommitID,
	}, nil
}

func (c *officialClient) CreateFile(ctx context.Context, repoPath, branch, filePath, commitMsg string, content io.Reader) error {
	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return New(KindConfig, "CreateFile", err)
	}
	encoded := base64.StdEncoding.EncodeToString(contentBytes)
	encoding := "base64"
	_, _, err = c.client.RepositoryFiles.CreateFile(repoPath, filePath,
		&gitlab.CreateFileOptions{
			Branch:        &branch,
			Encoding:      &encoding,
			Content:       &encoded,
			CommitMessage: &commitMsg,
		},
		gitlab.WithContext(ctx))
	if err != nil {
		return classifyError("CreateFile", err)
	}
	return nil
}

func (c *officialClient) UpdateFile(ctx context.Context, repoPath, branch, filePath, commitMsg, lastCommitID string, content io.Reader) error {
	contentBytes, err := io.ReadAll(content)
	if err != nil {
		return New(KindConfig, "UpdateFile", err)
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

func (c *officialClient) ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*MergeRequest, error) {
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
	return &MergeRequest{
		IID:          int(first.IID),
		SourceBranch: first.SourceBranch,
		TargetBranch: first.TargetBranch,
		Title:        first.Title,
		Description:  first.Description,
		WebURL:       first.WebURL,
	}, nil
}

func (c *officialClient) CreateMR(ctx context.Context, repoPath string, in CreateMRInput) (*MergeRequest, error) {
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
	return &MergeRequest{
		IID:          int(mr.IID),
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
		Title:        mr.Title,
		Description:  mr.Description,
		WebURL:       mr.WebURL,
	}, nil
}

// classifyError maps an error from the official client into a typed *Error
// carrying the appropriate Kind. Special handling for ErrNotFound (the
// official client returns this for 404s without an embedded response) and
// for net.Error (any transient network failure).
func classifyError(op string, err error) error {
	if err == nil {
		return nil
	}
	// The official client returns ErrNotFound directly when 404 is hit and
	// the per-method code path doesn't construct an ErrorResponse.
	if errors.Is(err, gitlab.ErrNotFound) {
		return New(KindNotFound, op, err)
	}
	var er *gitlab.ErrorResponse
	if errors.As(err, &er) {
		status := er.Response.StatusCode
		kind := ClassifyStatus(status)
		return &Error{Kind: kind, Op: op, Err: err, StatusCode: status}
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return &Error{Kind: KindTransient, Op: op, Err: err}
	}
	return &Error{Kind: KindUnknown, Op: op, Err: err}
}
