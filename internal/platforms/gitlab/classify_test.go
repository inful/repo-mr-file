package gitlab

import (
	"errors"
	"net/http"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go"

	"github.com/inful/repo-mr-file/internal/platforms"
)

// TestClassifyError_RetryAfterFromResponse verifies that a GitLab error
// response carrying Retry-After surfaces that value on the typed error.
func TestClassifyError_RetryAfterFromResponse(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"17"}},
	}
	glErr := &gitlab.ErrorResponse{Response: resp, Message: "rate limit"}
	out := classifyError("GetProject", glErr)
	e := platforms.As(out)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %T: %v", out, out)
	}
	if e.Kind != platforms.KindTransient {
		t.Errorf("Kind = %v, want KindTransient", e.Kind)
	}
	if e.RetryAfter.Seconds() != 17 {
		t.Errorf("RetryAfter = %v, want 17s", e.RetryAfter)
	}
}

// TestClassifyError_NoRetryHeader keeps the field zero when the server
// didn't supply Retry-After (the same path that produces 4xx errors
// like 401, 403, 404, 409).
func TestClassifyError_NoRetryHeader(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{},
	}
	glErr := &gitlab.ErrorResponse{Response: resp, Message: "not found"}
	out := classifyError("GetBranch", glErr)
	e := platforms.As(out)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %v", out)
	}
	if e.Kind != platforms.KindNotFound {
		t.Errorf("Kind = %v, want KindNotFound", e.Kind)
	}
	if e.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0", e.RetryAfter)
	}
}

// TestClassifyError_RetryAfterCappedAtMax verifies the value is capped.
func TestClassifyError_RetryAfterCappedAtMax(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Retry-After": []string{"9999"}},
	}
	out := classifyError("UpdateFile", &gitlab.ErrorResponse{Response: resp, Message: "down"})
	e := platforms.As(out)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %v", out)
	}
	if e.RetryAfter != platforms.MaxRetryAfter {
		t.Errorf("RetryAfter = %v, want %v (capped)", e.RetryAfter, platforms.MaxRetryAfter)
	}
}

// TestClassifyError_NotFoundStillClassifiedCorrectly ensures the
// Retry-After plumbing doesn't disturb the ErrNotFound short-circuit.
func TestClassifyError_NotFoundStillClassifiedCorrectly(t *testing.T) {
	out := classifyError("GetFile", gitlab.ErrNotFound)
	e := platforms.As(out)
	if e == nil {
		t.Fatalf("expected *platforms.Error, got %v", out)
	}
	if e.Kind != platforms.KindNotFound {
		t.Errorf("Kind = %v, want KindNotFound", e.Kind)
	}
	if !errors.Is(out, gitlab.ErrNotFound) {
		t.Errorf("expected wrapped gitlab.ErrNotFound, got %v", out)
	}
}
