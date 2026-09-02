package gitea

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inful/repo-mr-file/internal/platforms"
)

// TestGitea_RetryAfter_PopulatesOnTransient verifies that a 429 response
// with a Retry-After: N header produces a KindTransient error carrying
// RetryAfter = N seconds on the typed *platforms.Error.
func TestGitea_RetryAfter_PopulatesOnTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"too many requests"}`))
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "t")
	_, err := c.GetProject(context.Background(), "foo/bar")
	if err == nil {
		t.Fatal("expected error")
	}
	e := platforms.As(err)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %T: %v", err, err)
	}
	if e.Kind != platforms.KindTransient {
		t.Errorf("Kind = %v, want KindTransient", e.Kind)
	}
	if e.RetryAfter.Seconds() != 42 {
		t.Errorf("RetryAfter = %v, want 42s", e.RetryAfter)
	}
}

// TestGitea_RetryAfter_CappedAtMax verifies the parsed value is capped
// at platforms.MaxRetryAfter.
func TestGitea_RetryAfter_CappedAtMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9999")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "t")
	_, err := c.GetProject(context.Background(), "foo/bar")
	e := platforms.As(err)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %v", err)
	}
	if e.RetryAfter != platforms.MaxRetryAfter {
		t.Errorf("RetryAfter = %v, want %v (capped)", e.RetryAfter, platforms.MaxRetryAfter)
	}
}

// TestGitea_RetryAfter_NotSet when the server didn't include the header,
// the typed error's RetryAfter stays zero.
func TestGitea_RetryAfter_NotSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "t")
	_, err := c.GetProject(context.Background(), "foo/bar")
	e := platforms.As(err)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %v", err)
	}
	if e.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0", e.RetryAfter)
	}
}

// TestGitea_RetryAfter_OnSuccessfulResponse does not crash when the
// server omits the Retry-After header on a 200 (just guards against
// re-ordering the response-header parse path).
func TestGitea_RetryAfter_OnSuccessfulResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Retry-After, 200 OK.
		_, _ = w.Write([]byte(`{"id":1,"default_branch":"main"}`))
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL, "t")
	_, err := c.GetProject(context.Background(), "foo/bar")
	if err != nil {
		// Just ensure we didn't crash from header access; details
		// of the success path are covered by other tests.
		if !strings.Contains(err.Error(), "decode") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
