package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inful/repo-mr-file/internal/platforms"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// writeJSON is a small helper used by the test server handlers.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestOfficialClient_GetProject_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is decoded; the official client sends foo%2Fbar which
		// net/http decodes to foo/bar.
		if !strings.HasSuffix(r.URL.Path, "/projects/foo/bar") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":             42,
			"default_branch": "main",
			"web_url":        "https://gitlab.example.com/foo/bar",
		})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
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

func TestOfficialClient_GetProject_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Project Not Found"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	_, err := c.GetProject(context.Background(), "missing/project")
	if err == nil {
		t.Fatal("expected error")
	}
	e := platforms.As(err)
	if e == nil {
		t.Fatalf("error is not a *platforms.Error: %v", err)
	}
	if e.Kind != platforms.KindNotFound {
		t.Errorf("Kind = %v, want KindNotFound", e.Kind)
	}
}

func TestOfficialClient_GetBranch_NotFound_ReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	exists, err := c.GetBranch(context.Background(), "foo/bar", "missing")
	if err != nil {
		t.Fatalf("GetBranch: %v", err)
	}
	if exists {
		t.Error("exists = true, want false for 404")
	}
}

func TestOfficialClient_GetFile_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repository/files/ca.pem") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		// GitLab returns base64-encoded content.
		encoded := "aGVsbG8K" // "hello\n"
		writeJSON(t, w, http.StatusOK, map[string]any{
			"file_path":      "ca.pem",
			"content":        encoded,
			"encoding":       "base64",
			"last_commit_id": "deadbeef",
			"ref":            "main",
		})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	f, err := c.GetFile(context.Background(), "foo/bar", "ca.pem", "main")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if string(f.Content) != "hello\n" {
		t.Errorf("Content = %q, want %q", f.Content, "hello\n")
	}
	if f.LastCommitID != "deadbeef" {
		t.Errorf("LastCommitID = %q, want deadbeef", f.LastCommitID)
	}
}

func TestOfficialClient_RetryOver503Then200(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			writeJSON(t, w, http.StatusServiceUnavailable, map[string]any{"message": "try again"})
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"id":             7,
			"default_branch": "main",
		})
	}))
	defer srv.Close()

	oc := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	c := platforms.WithRetry(oc, platforms.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Microsecond})

	p, err := c.GetProject(context.Background(), "foo/bar")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.ID != 7 {
		t.Errorf("ID = %d, want 7", p.ID)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("server calls = %d, want 2", got)
	}
}

func TestOfficialClient_CreateFile_SendsBase64(t *testing.T) {
	var seenBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody.Store(string(body))
		writeJSON(t, w, http.StatusCreated, map[string]any{"file_path": "ca.pem"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	if err := c.CreateFile(context.Background(), "foo/bar", "branch", "ca.pem", "main", "msg", strings.NewReader("hello")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	body, _ := seenBody.Load().(string)
	if !strings.Contains(body, `"encoding":"base64"`) {
		t.Errorf("body missing encoding=base64: %s", body)
	}
	if !strings.Contains(body, `"content":"aGVsbG8="`) {
		t.Errorf("body missing base64-encoded content: %s", body)
	}
	if !strings.Contains(body, `"branch":"branch"`) {
		t.Errorf("body missing branch: %s", body)
	}
}

func TestOfficialClient_UpdateFile_Conflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]any{"message": "stale branch head"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	err := c.UpdateFile(context.Background(), "foo/bar", "branch", "ca.pem", "main", "msg", "deadbeef", strings.NewReader("hello"))
	if err == nil {
		t.Fatal("expected conflict error")
	}
	e := platforms.As(err)
	if e == nil || e.Kind != platforms.KindConflict {
		t.Errorf("Kind = %v, want KindConflict (err = %v)", e, err)
	}
}

func TestOfficialClient_BadBaseURL(t *testing.T) {
	c := NewOfficialClient("://bad-url", "test-token")
	_, err := c.GetProject(context.Background(), "foo/bar")
	if err == nil {
		t.Fatal("expected error from malformed URL")
	}
	e := platforms.As(err)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %T: %v", err, err)
	}
	if e.Kind != platforms.KindConfig {
		t.Errorf("Kind = %v, want KindConfig", e.Kind)
	}
}

// TestOfficialClient_CreateBranch_AlreadyExists_IsIdempotent verifies that
// POST /repository/branches returning 400 with the
// "has already been taken" body is treated as success. This is the
// common case when a concurrent run beat us to the same branch.
func TestOfficialClient_CreateBranch_AlreadyExists_IsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		// Real-world GitLab body for duplicate branch: validation
		// error on the `name` field with the Rails "has already
		// been taken" phrasing. See
		// https://docs.gitlab.com/ee/api/branches.html#create-repository-branch
		_, _ = w.Write([]byte(`{"message":{"name":["has already been taken"]}}`))
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	if err := c.CreateBranch(context.Background(), "foo/bar", "new", "main"); err != nil {
		t.Fatalf("CreateBranch: %v (idempotent 400 'already taken' must not be a hard error)", err)
	}
}

// TestOfficialClient_CreateBranch_400_NotAlreadyTaken_IsError locks in the
// bug fix: a 400 response whose body does NOT contain "has already been
// taken" (e.g. invalid branch name, missing ref) must surface as a
// real error. Previously the code treated any 400 as "already exists"
// and silently swallowed it; that masked genuine misconfigurations.
func TestOfficialClient_CreateBranch_400_NotAlreadyTaken_IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		// Invalid branch name — different field, different message.
		_, _ = w.Write([]byte(`{"message":{"branch_name":["is invalid"]}}`))
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	err := c.CreateBranch(context.Background(), "foo/bar", "weird name with $pecial chars", "main")
	if err == nil {
		t.Fatal("expected error: 400 with non-'already taken' body must not be silently swallowed")
	}
	e := platforms.As(err)
	if e == nil || e.Kind != platforms.KindConfig {
		t.Errorf("Kind = %v, want KindConfig (err = %v)", e, err)
	}
}

func TestOfficialClient_ListOpenMR_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	mr, err := c.ListOpenMR(context.Background(), "foo/bar", "src", "tgt")
	if err != nil {
		t.Fatalf("ListOpenMR: %v", err)
	}
	if mr != nil {
		t.Errorf("mr = %+v, want nil for empty list", mr)
	}
}

func TestOfficialClient_ListOpenMR_ConflictBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{
				"iid":           42,
				"source_branch": "src",
				"target_branch": "tgt",
				"title":         "Test MR",
				"web_url":       "https://gitlab.example.com/foo/bar/-/merge_requests/42",
			},
		})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	mr, err := c.ListOpenMR(context.Background(), "foo/bar", "src", "tgt")
	if err != nil {
		t.Fatalf("ListOpenMR: %v", err)
	}
	if mr == nil || mr.IID != 42 {
		t.Errorf("mr = %+v, want IID=42", mr)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want platforms.Kind
	}{
		{"nil", nil, platforms.KindUnknown},
		{"ErrNotFound", gitlab.ErrNotFound, platforms.KindNotFound},
		{"plain error", errors.New("plain"), platforms.KindUnknown},
		{"net.OpError", &net.OpError{Op: "dial", Err: errors.New("no route")}, platforms.KindTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyError("op", tc.err)
			if tc.err == nil {
				if got != nil {
					t.Errorf("classifyError(nil) = %v, want nil", got)
				}
				return
			}
			e := platforms.As(got)
			if e == nil || e.Kind != tc.want {
				t.Errorf("classifyError Kind = %v, want %v", e, tc.want)
			}
		})
	}
}

// TestClassifyError_AuthHints locks in the GitLab auth hint
// classification. GitLab's response body doesn't carry a structured
// token-state signal, so the hint is a 401-vs-403 split: 401 says
// the token is bad/expired/revoked; 403 says the token is valid but
// under-scoped. The kind stays KindAuth (exit 3) across both cases.
func TestClassifyError_AuthHints(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		wantSub string
	}{
		{"401", http.StatusUnauthorized, `{"message":"401 Unauthorized"}`, "401 Unauthorized"},
		{"403", http.StatusForbidden, `{"message":"403 Forbidden"}`, "403 Forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(tc.message)),
			}
			err := &gitlab.ErrorResponse{Response: resp, Message: tc.message}
			got := classifyError("GetProject", err)
			e := platforms.As(got)
			if e == nil {
				t.Fatalf("expected *platforms.Error, got %v", got)
			}
			if e.Kind != platforms.KindAuth {
				t.Errorf("Kind = %v, want KindAuth", e.Kind)
			}
			if e.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", e.StatusCode, tc.status)
			}
			if !strings.Contains(e.Hint, tc.wantSub) {
				t.Errorf("Hint = %q, want substring %q", e.Hint, tc.wantSub)
			}
			// Hint must replace the underlying message so the
			// operator reads the diagnostic.
			if !strings.Contains(got.Error(), tc.wantSub) {
				t.Errorf("Error() = %q, want substring %q", got.Error(), tc.wantSub)
			}
		})
	}
}
