package github

import (
	"errors"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v74/github"

	"github.com/inful/repo-mr-file/internal/platforms"
)

// TestClassifyErr_RetryAfterFromResponse verifies that when a GitHub
// error carries a Retry-After header, classifyErr copies it onto the
// typed platforms.Error's RetryAfter field.
func TestClassifyErr_RetryAfterFromResponse(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"30"}},
	}
	ghErr := &gh.ErrorResponse{
		Response: resp,
		Message:  "rate limit",
	}
	out := classifyErr("CreateFile", ghErr)
	e := platforms.As(out)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %T: %v", out, out)
	}
	if e.Kind != platforms.KindTransient {
		t.Errorf("Kind = %v, want KindTransient", e.Kind)
	}
	if e.RetryAfter.Seconds() != 30 {
		t.Errorf("RetryAfter = %v, want 30s", e.RetryAfter)
	}
}

// TestClassifyErr_NoRetryAfter verifies the field stays zero when the
// server omits the header (or when the error isn't a *gh.ErrorResponse).
func TestClassifyErr_NoRetryAfter(t *testing.T) {
	t.Run("plain_error", func(t *testing.T) {
		out := classifyErr("ListOpenMR", errors.New("connection refused"))
		e := platforms.As(out)
		if e == nil {
			t.Fatalf("expected *platforms.Error, got %v", out)
		}
		if e.Kind != platforms.KindTransient {
			t.Errorf("Kind = %v, want KindTransient", e.Kind)
		}
		if e.RetryAfter != 0 {
			t.Errorf("RetryAfter = %v, want 0", e.RetryAfter)
		}
	})

	t.Run("response_without_header", func(t *testing.T) {
		resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
		ghErr := &gh.ErrorResponse{Response: resp, Message: "forbidden"}
		out := classifyErr("GetProject", ghErr)
		e := platforms.As(out)
		if e == nil {
			t.Fatalf("expected *platforms.Error, got %v", out)
		}
		if e.RetryAfter != 0 {
			t.Errorf("RetryAfter = %v, want 0", e.RetryAfter)
		}
	})
}

// TestClassifyErr_RetryAfterCapped verifies the value on the typed error
// is capped at platforms.MaxRetryAfter.
func TestClassifyErr_RetryAfterCapped(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Retry-After": []string{"9999"}},
	}
	out := classifyErr("UpdateFile", &gh.ErrorResponse{Response: resp, Message: "down"})
	e := platforms.As(out)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %v", out)
	}
	if e.RetryAfter != platforms.MaxRetryAfter {
		t.Errorf("RetryAfter = %v, want %v (capped)", e.RetryAfter, platforms.MaxRetryAfter)
	}
}
