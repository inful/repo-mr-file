package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v74/github"

	"github.com/inful/repo-mr-file/internal/platforms"
)

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// newClient builds a GitHub client whose HTTP transport points at srv.
func newClient(t *testing.T, srv *httptest.Server, token string) *client {
	t.Helper()
	httpClient := srv.Client()
	oc, err := NewClient(github.NewClient(httpClient), "octocat", token, srv.URL+"/")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// NewClient returns platforms.Client (the public contract);
	// tests in this package use the concrete *client to access
	// fields like c.user. Type-assert back here.
	c, ok := oc.(*client)
	if !ok {
		t.Fatalf("NewClient returned unexpected type %T", oc)
	}
	return c
}

func TestGitHub_GetProject_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/octocat/hello" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":             42,
			"default_branch": "main",
		})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	p, err := c.GetProject(context.Background(), "octocat/hello")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.ID != 42 {
		t.Errorf("ID = %d, want 42", p.ID)
	}
	if p.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want main", p.DefaultBranch)
	}
}

func TestGitHub_GetBranch_NotFound_ReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/octocat/hello/branches/missing" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	exists, err := c.GetBranch(context.Background(), "octocat/hello", "missing")
	if err != nil {
		t.Fatalf("GetBranch: %v", err)
	}
	if exists {
		t.Error("exists = true, want false for 404")
	}
}

func TestGitHub_GetFile_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/octocat/hello/contents/ca.pem" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("ref") != "main" {
			t.Errorf("expected ref=main, got %q", r.URL.Query().Get("ref"))
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"name":     "ca.pem",
			"path":     "ca.pem",
			"content":  base64.StdEncoding.EncodeToString([]byte("hello\n")),
			"encoding": "base64",
			"sha":      "blob-sha",
		})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	f, err := c.GetFile(context.Background(), "octocat/hello", "ca.pem", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(f.Content) != "hello\n" {
		t.Errorf("Content = %q, want %q", f.Content, "hello\n")
	}
	if f.LastCommitID != "blob-sha" {
		t.Errorf("LastCommitID = %q, want blob-sha", f.LastCommitID)
	}
}

func TestGitHub_CreateFile_Success(t *testing.T) {
	// Mock server models GitHub's real behavior: a GET on a non-existent
	// branch returns 404, and PUT /contents requires the branch to already
	// exist. The client must pre-create the branch before the file POST.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branches/update-v1"):
			// The client probes for the target branch first; report
			// 404 so it knows the branch is missing and creates it.
			writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branches/main"):
			writeJSON(t, w, http.StatusOK, map[string]any{
				"name": "main", "commit": map[string]any{"sha": "parent-sha-1234"},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"ref":"refs/heads/update-v1"`) {
				t.Errorf("ref POST missing ref header: %s", body)
			}
			if !strings.Contains(string(body), `"sha":"parent-sha-1234"`) {
				t.Errorf("ref POST missing parent sha: %s", body)
			}
			writeJSON(t, w, http.StatusCreated, map[string]any{
				"ref":    "refs/heads/update-v1",
				"object": map[string]any{"sha": "new-branch-sha"},
			})
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/contents/ca.pem"):
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"branch":"update-v1"`) {
				t.Errorf("body missing branch: %s", body)
			}
			if !strings.Contains(string(body), `"content":"aGVsbG8K"`) {
				t.Errorf("body missing base64 content: %s", body)
			}
			if !strings.Contains(string(body), `"message":"msg"`) {
				t.Errorf("body missing message: %s", body)
			}
			writeJSON(t, w, http.StatusCreated, map[string]any{
				"content": map[string]any{"name": "ca.pem", "sha": "new-sha"},
			})
		default:
			http.Error(w, fmt.Sprintf("unexpected: %s %s", r.Method, r.URL.Path), http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	err := c.CreateFile(context.Background(), "octocat/hello", "update-v1", "ca.pem", "main", "msg", strings.NewReader("hello\n"))
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
}

func TestGitHub_UpdateFile_SendsSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"sha":"old-blob-sha"`) {
			t.Errorf("body missing sha: %s", body)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"content": map[string]any{"name": "ca.pem", "sha": "new-sha"},
		})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	err := c.UpdateFile(context.Background(), "octocat/hello", "main", "ca.pem", "main", "msg", "old-blob-sha", strings.NewReader("new"))
	if err != nil {
		t.Fatalf("UpdateFile: %v", err)
	}
}

func TestGitHub_UpdateFile_Conflict_ClassifiedAsConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnprocessableEntity, map[string]any{"message": "sha mismatch"})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	err := c.UpdateFile(context.Background(), "octocat/hello", "main", "ca.pem", "main", "msg", "old", strings.NewReader("new"))
	if err == nil {
		t.Fatal("expected conflict error")
	}
	e := platforms.As(err)
	if e == nil || e.Kind != platforms.KindConflict {
		t.Errorf("Kind = %v, want KindConflict", e)
	}
}

func TestGitHub_ListOpenMR_UsesUserBranchFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/octocat/hello/pulls" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		head := r.URL.Query().Get("head")
		if head != "octocat:update-v1" {
			t.Errorf("head = %q, want octocat:update-v1", head)
		}
		base := r.URL.Query().Get("base")
		if base != "main" {
			t.Errorf("base = %q, want main", base)
		}
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("state = %q, want open", r.URL.Query().Get("state"))
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"number":   77,
				"html_url": "https://github.com/octocat/hello/pull/77",
				"head":     map[string]any{"ref": "update-v1"},
				"title":    "Test PR",
				"body":     "body text",
				"state":    "open",
			},
		})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	mr, err := c.ListOpenMR(context.Background(), "octocat/hello", "update-v1", "main")
	if err != nil {
		t.Fatalf("ListOpenMR: %v", err)
	}
	if mr == nil {
		t.Fatal("expected MR, got nil")
	}
	if mr.IID != 77 {
		t.Errorf("IID = %d, want 77", mr.IID)
	}
	if mr.WebURL != "https://github.com/octocat/hello/pull/77" {
		t.Errorf("WebURL = %q", mr.WebURL)
	}
}

func TestGitHub_ListOpenMR_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	mr, err := c.ListOpenMR(context.Background(), "octocat/hello", "update-v1", "main")
	if err != nil {
		t.Fatalf("ListOpenMR: %v", err)
	}
	if mr != nil {
		t.Errorf("mr = %+v, want nil for empty list", mr)
	}
}

func TestGitHub_CreateMR_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v3/repos/octocat/hello/pulls" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"head":"octocat:update-v1"`) {
			t.Errorf("body missing head: %s", body)
		}
		if !strings.Contains(string(body), `"base":"main"`) {
			t.Errorf("body missing base: %s", body)
		}
		if !strings.Contains(string(body), `"title":"Test PR"`) {
			t.Errorf("body missing title: %s", body)
		}
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"number":   88,
			"html_url": "https://github.com/octocat/hello/pull/88",
			"head":     map[string]any{"ref": "update-v1"},
		})
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	mr, err := c.CreateMR(context.Background(), "octocat/hello", platforms.CreateMRInput{
		SourceBranch: "update-v1",
		TargetBranch: "main",
		Title:        "Test PR",
		Description:  "body text",
	})
	if err != nil {
		t.Fatalf("CreateMR: %v", err)
	}
	if mr.IID != 88 {
		t.Errorf("IID = %d, want 88", mr.IID)
	}
}

func TestGitHub_AuthHeader(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 1, "default_branch": "main"})
	}))
	defer srv.Close()

	c := newClient(t, srv, "secret-token")
	if _, err := c.GetProject(context.Background(), "octocat/hello"); err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if seenAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want 'Bearer secret-token'", seenAuth)
	}
}

func TestSplitRepoPath(t *testing.T) {
	cases := []struct {
		in        string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"octocat/hello", "octocat", "hello", false},
		{"group/subgroup/repo", "group", "subgroup/repo", false},
		{"single", "", "", true},
		{"", "", "", true},
	}
	for _, tc := range cases {
		owner, repo, err := splitRepoPath(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("splitRepoPath(%q): expected error, got owner=%q repo=%q", tc.in, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitRepoPath(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if owner != tc.wantOwner {
			t.Errorf("splitRepoPath(%q) owner = %q, want %q", tc.in, owner, tc.wantOwner)
		}
		if repo != tc.wantRepo {
			t.Errorf("splitRepoPath(%q) repo = %q, want %q", tc.in, repo, tc.wantRepo)
		}
	}
}

// silence unused imports in case future edits remove usage
var (
	_ = fmt.Sprintf
	_ = url.PathEscape
)
