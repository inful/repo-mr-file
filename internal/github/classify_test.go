package github

import (
	"errors"
	"net/http"
	"strings"
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

// TestClassifyErr_AuthHints locks in the four-way GitHub auth hint
// classification. Each case builds a synthetic *gh.ErrorResponse
// and asserts the hint string operators will see in the log line.
// Kind stays KindAuth (exit 3) across all cases — only the hint
// differs.
func TestClassifyErr_AuthHints(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		header  http.Header
		wantSub string
	}{
		{
			name:    "401_bad_credentials",
			status:  http.StatusUnauthorized,
			message: "Bad credentials",
			header:  http.Header{},
			wantSub: "401 Bad credentials",
		},
		{
			name:    "401_generic",
			status:  http.StatusUnauthorized,
			message: "Requires authentication",
			header:  http.Header{},
			wantSub: "401 Unauthorized",
		},
		{
			name:    "403_rate_limited",
			status:  http.StatusForbidden,
			message: "API rate limit exceeded for user ID 12345.",
			header: http.Header{
				// Use the canonical MIME header form: the canonical
				// form of "X-RateLimit-Remaining" is
				// "X-Ratelimit-Remaining" (only the R after `-` is
				// upper-case; inner capitals are lowercased by
				// textproto.CanonicalMIMEHeaderKey). http.Header.Get
				// canonicalizes its argument but a map literal does
				// NOT canonicalize the stored key, so we have to use
				// the canonical form here.
				"X-Ratelimit-Remaining": []string{"0"},
				"X-Ratelimit-Reset":     []string{"1700000000"},
			},
			wantSub: "rate limit exceeded",
		},
		{
			name:    "403_fine_grained_under_scoped",
			status:  http.StatusForbidden,
			message: "Resource not accessible by personal access token",
			header:  http.Header{},
			wantSub: "fine-grained",
		},
		{
			name:    "403_generic_insufficient_permission",
			status:  http.StatusForbidden,
			message: "Forbidden",
			header:  http.Header{},
			wantSub: "lacks write access",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: tc.header}
			out := classifyErr("GetProject", &gh.ErrorResponse{Response: resp, Message: tc.message})
			e := platforms.As(out)
			if e == nil {
				t.Fatalf("expected *platforms.Error, got %T: %v", out, out)
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
			// Hint must replace the underlying error message so
			// the operator reads the diagnostic instead of the
			// bare "GetProject: 401 Bad credentials" form.
			gotErr := e.Error()
			if !strings.Contains(gotErr, tc.wantSub) {
				t.Errorf("Error() = %q, want substring %q", gotErr, tc.wantSub)
			}
			if !strings.HasPrefix(gotErr, "GetProject: ") {
				t.Errorf("Error() = %q, want GetProject: prefix", gotErr)
			}
		})
	}
}
