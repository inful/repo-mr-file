package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestGitea_GetProject_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/foo/bar" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":             42,
			"default_branch": "main",
		})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	p, err := c.GetProject(context.Background(), "foo/bar")
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

func TestGitea_GetBranch_NotFound_ReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Branch Not Found"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	exists, err := c.GetBranch(context.Background(), "foo/bar", "missing")
	if err != nil {
		t.Fatalf("GetBranch: %v", err)
	}
	if exists {
		t.Error("exists = true, want false for 404")
	}
}

func TestGitea_GetFile_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "main" {
			t.Errorf("expected ref=main, got %q", r.URL.Query().Get("ref"))
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"name":            "ca.pem",
			"path":            "ca.pem",
			"content":         base64.StdEncoding.EncodeToString([]byte("hello\n")),
			"encoding":        "base64",
			"sha":             "blob-sha",
			"last_commit_sha": "commit-sha",
		})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	f, err := c.GetFile(context.Background(), "foo/bar", "ca.pem", "main")
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

func TestGitea_CreateFile_CreatesBranchFirst(t *testing.T) {
	var createdBranch, createdFile bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/foo/bar":
			// GetProject — needed to find the default branch to branch from
			writeJSON(t, w, http.StatusOK, map[string]any{
				"id":             1,
				"default_branch": "main",
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/update-v1"):
			// GetBranch → 404, branch doesn't exist yet
			writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/branches"):
			// CreateBranch
			createdBranch = true
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"new_branch_name":"update-v1"`) {
				t.Errorf("CreateBranch body missing new_branch_name: %s", body)
			}
			if !strings.Contains(string(body), `"old_branch_name":"main"`) {
				t.Errorf("CreateBranch body missing old_branch_name=main: %s", body)
			}
			writeJSON(t, w, http.StatusCreated, map[string]any{"name": "update-v1"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/contents/"):
			// CreateFile
			createdFile = true
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"content":"aGVsbG8K"`) {
				t.Errorf("body missing base64-encoded content: %s", body)
			}
			if !strings.Contains(string(body), `"branch":"update-v1"`) {
				t.Errorf("body missing branch: %s", body)
			}
			writeJSON(t, w, http.StatusCreated, map[string]any{"content": map[string]any{"name": "ca.pem"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	err := c.CreateFile(context.Background(), "foo/bar", "update-v1", "ca.pem", "msg", strings.NewReader("hello\n"))
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if !createdBranch {
		t.Error("expected CreateBranch to be called before CreateFile")
	}
	if !createdFile {
		t.Error("expected CreateFile to be called")
	}
}

func TestGitea_CreateFile_BranchAlreadyExists_SkipsCreateBranch(t *testing.T) {
	var createdBranch, createdFile bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/branches/update-v1"):
			// GetBranch → 200, branch exists; skip CreateBranch
			writeJSON(t, w, http.StatusOK, map[string]any{"name": "update-v1"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/branches"):
			createdBranch = true
			writeJSON(t, w, http.StatusInternalServerError, map[string]any{"message": "should not be called"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/contents/"):
			createdFile = true
			writeJSON(t, w, http.StatusCreated, map[string]any{"content": map[string]any{"name": "ca.pem"}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	err := c.CreateFile(context.Background(), "foo/bar", "update-v1", "ca.pem", "msg", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if createdBranch {
		t.Error("CreateBranch should NOT have been called (branch already exists)")
	}
	if !createdFile {
		t.Error("CreateFile should have been called")
	}
}

func TestGitea_UpdateFile_SendsSHA(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		writeJSON(t, w, http.StatusOK, map[string]any{"content": map[string]any{"name": "ca.pem"}})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	err := c.UpdateFile(context.Background(), "foo/bar", "main", "ca.pem", "msg", "blob-sha-old", strings.NewReader("new"))
	if err != nil {
		t.Fatalf("UpdateFile: %v", err)
	}
	if !strings.Contains(seenBody, `"sha":"blob-sha-old"`) {
		t.Errorf("body missing sha: %s", seenBody)
	}
}

func TestGitea_UpdateFile_Conflict_ClassifiedAsConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnprocessableEntity, map[string]any{"message": "sha mismatch"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	err := c.UpdateFile(context.Background(), "foo/bar", "main", "ca.pem", "msg", "old", strings.NewReader("new"))
	if err == nil {
		t.Fatal("expected error")
	}
	e := platforms.As(err)
	if e == nil || e.Kind != platforms.KindConflict {
		t.Errorf("Kind = %v, want KindConflict", e)
	}
}

func TestGitea_ListOpenMR_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/pulls/main/update-v1") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"id":       88,
				"number":   88,
				"html_url": "https://gitea.example.com/foo/bar/pulls/88",
			},
		})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	mr, err := c.ListOpenMR(context.Background(), "foo/bar", "update-v1", "main")
	if err != nil {
		t.Fatalf("ListOpenMR: %v", err)
	}
	if mr == nil {
		t.Fatal("expected MR, got nil")
	}
	if mr.IID != 88 {
		t.Errorf("IID = %d, want 88", mr.IID)
	}
	if mr.WebURL != "https://gitea.example.com/foo/bar/pulls/88" {
		t.Errorf("WebURL = %q, want expected URL", mr.WebURL)
	}
}

func TestGitea_ListOpenMR_NotFound_ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gitea returns 404 for the by-base-head endpoint when no match
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	mr, err := c.ListOpenMR(context.Background(), "foo/bar", "update-v1", "main")
	if err != nil {
		t.Fatalf("ListOpenMR: %v", err)
	}
	if mr != nil {
		t.Errorf("mr = %+v, want nil for 404", mr)
	}
}

func TestGitea_CreateMR_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"head":"update-v1"`) {
			t.Errorf("body missing head: %s", body)
		}
		if !strings.Contains(string(body), `"base":"main"`) {
			t.Errorf("body missing base: %s", body)
		}
		writeJSON(t, w, http.StatusCreated, map[string]any{
			"id":       99,
			"number":   99,
			"html_url": "https://gitea.example.com/foo/bar/pulls/99",
		})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "test-token")
	mr, err := c.CreateMR(context.Background(), "foo/bar", platforms.CreateMRInput{
		SourceBranch: "update-v1",
		TargetBranch: "main",
		Title:        "Test",
		Description:  "body",
	})
	if err != nil {
		t.Fatalf("CreateMR: %v", err)
	}
	if mr.IID != 99 {
		t.Errorf("IID = %d, want 99", mr.IID)
	}
}

func TestGitea_AuthHeader(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 1, "default_branch": "main"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "secret-token")
	_, err := c.GetProject(context.Background(), "foo/bar")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	wantPrefix := "token "
	if !strings.HasPrefix(seenAuth, wantPrefix) {
		t.Errorf("Authorization = %q, want prefix %q", seenAuth, wantPrefix)
	}
	if seenAuth != "token secret-token" {
		t.Errorf("Authorization = %q, want %q", seenAuth, "token secret-token")
	}
}

func TestGitea_BadBaseURL(t *testing.T) {
	// Use a URL that is malformed in a way NewRequest will reject.
	c := NewOfficialClient("://bad-url", "test-token")
	_, err := c.GetProject(context.Background(), "foo/bar")
	if err == nil {
		t.Fatal("expected error from malformed URL")
	}
}

// silence unused import in case fmt becomes unused after future edits.
var _ = fmt.Sprintf
