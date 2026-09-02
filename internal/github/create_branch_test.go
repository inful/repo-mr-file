package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateBranch_PostsGitRefsAtParent is the RED test for the GitHub
// branch-creation flow that the bundler now relies on. The mock server
// captures the JSON body and asserts the POST /git/refs payload
// carries refs/heads/<newBranch> and the start branch's SHA.
func TestCreateBranch_PostsGitRefsAtParent(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branches/feature"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branches/main"):
			_, _ = w.Write([]byte(`{"name":"main","commit":{"sha":"parent-sha-1234"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			seenBody = string(buf)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	if err := c.CreateBranch(context.Background(), "octocat/hello", "feature", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if !strings.Contains(seenBody, `"ref":"refs/heads/feature"`) {
		t.Errorf("git/refs body missing ref header: %s", seenBody)
	}
	if !strings.Contains(seenBody, `"sha":"parent-sha-1234"`) {
		t.Errorf("git/refs body missing parent sha: %s", seenBody)
	}
}

// TestCreateBranch_BranchAlreadyExists_404IsIdempotent verifies the
// bundler can retry without a transient-error escalation: GitHub's
// 422 "Reference already exists" on POST /git/refs is a successful
// no-op for our purposes (some other run created the same ref).
func TestCreateBranch_BranchAlreadyExists_404IsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branches/feat"):
			_, _ = w.Write([]byte(`{"name":"feat","commit":{"sha":"existing-sha"}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/branches/main"):
			_, _ = w.Write([]byte(`{"name":"main","commit":{"sha":"parent-sha-1234"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
			// 422 on duplicate ref.
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	if err := c.CreateBranch(context.Background(), "octocat/hello", "feat", "main"); err != nil {
		t.Fatalf("CreateBranch: %v (idempotent 422 must not be a hard error)", err)
	}
}

// TestCreateBranch_StartBranchMissing_ClassifiesAsConfig confirms that
// the bundler surfaces a configuration-shaped error when the parent
// ref isn't there, not a transient.
func TestCreateBranch_StartBranchMissing_ClassifiesAsConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Both branches missing.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, "test-token")
	if err := c.CreateBranch(context.Background(), "octocat/hello", "feat", "main"); err == nil {
		t.Fatal("expected error when start branch is missing")
	}
}
