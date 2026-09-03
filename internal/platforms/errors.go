// Package platforms provides typed errors, retry logic, a recording client,
// and a dry-run stub. The Client interface is the only contract the bundler
// depends on; per-platform implementations live in sub-packages.
//
// A WithRetry wrapper decorates any Client with the configured retry policy
// (5xx / 429 / network errors only, with exponential backoff and jitter).
package platforms

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Kind classifies an error so the caller can map it to a process exit code.
// See the README exit code table for the mapping.
type Kind int

const (
	// KindUnknown is the zero value; treat as "no classification available".
	KindUnknown Kind = iota
	// KindConfig covers 400-class errors that aren't auth/not-found/conflict
	// (e.g. a bare 400 with a "branch is empty" or "invalid ref" body).
	// Use for input / request-shape mistakes.
	KindConfig
	// KindAuth covers 401 and 403.
	KindAuth
	// KindNotFound covers 404.
	KindNotFound
	// KindConflict covers 409 (e.g. stale file branch head on UpdateFile)
	// AND 422, which GitHub and Gitea/Forgejo use to signal the same
	// condition that GitLab reports as 409. See ClassifyStatus for the
	// mapping rationale.
	KindConflict
	// KindTransient covers 429, 5xx, and network errors — all worth retrying.
	KindTransient
	// KindInternal covers programmer errors / decoding failures / unexpected.
	KindInternal
)

// String returns a stable name for k, useful for structured logging.
func (k Kind) String() string {
	switch k {
	case KindConfig:
		return "config"
	case KindAuth:
		return "auth"
	case KindNotFound:
		return "not-found"
	case KindConflict:
		return "conflict"
	case KindTransient:
		return "transient"
	case KindInternal:
		return "internal"
	default:
		return "unknown"
	}
}

// Error is a typed error carrying a Kind so callers can branch on the
// failure category and map it to a process exit code.
type Error struct {
	Kind       Kind
	Op         string // the operation that failed (e.g. "GetProject")
	Err        error  // underlying cause
	StatusCode int    // HTTP status code, 0 if not applicable
	// RetryAfter is the server-supplied wait hint (parsed from the
	// Retry-After header on transient-class responses). 0 means "no
	// hint; use the configured exponential backoff". WithRetry honors
	// this value (capped at MaxRetryAfter) instead of the backoff.
	RetryAfter time.Duration
	// Hint is an operator-friendly, human-readable diagnostic for
	// this error. When non-empty, Error() returns "<Op>: <Hint>"
	// (or just "<Hint>" when Op is empty), so the operator sees
	// the actionable guidance directly in the log line instead of
	// the raw "<Op>: <underlying err>".
	//
	// Hint is currently populated only for KindAuth errors by the
	// per-platform classify functions; it distinguishes
	// expired/revoked tokens from insufficient scope (GitHub can
	// distinguish the four cases via body + headers; GitLab and
	// Gitea/Forgejo fall back to a 401-vs-403 split because their
	// response bodies don't carry a structured signal).
	Hint string
}

// Error returns "<Op>: <Hint>" when Hint is set (so the operator reads
// the actionable diagnostic directly), otherwise the original
// "<Op>: <underlying err>" format. Existing tests and log-grep
// pipelines continue to work because Hint defaults to empty.
func (e *Error) Error() string {
	if e.Hint != "" {
		if e.Op == "" {
			return e.Hint
		}
		return fmt.Sprintf("%s: %s", e.Op, e.Hint)
	}
	if e.Op == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Err.Error())
}

func (e *Error) Unwrap() error { return e.Err }

// Is allows errors.Is(err, &Error{Kind: KindAuth}) to match any error with
// the same Kind, regardless of Op / Err / StatusCode.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Kind == other.Kind
}

// New constructs a *Error with the given kind and underlying cause.
func New(kind Kind, op string, err error) *Error {
	return &Error{Kind: kind, Op: op, Err: err}
}

// ClassifyStatus maps an HTTP status code to a Kind. 4xx codes other than
// 401/403/404/409/422/429 fall into KindConfig (input mistakes), and
// 5xx/429 fall into KindTransient (worth retrying).
//
// 422 is mapped to KindConflict (not KindConfig) because GitHub and
// Gitea/Forgejo use 422 to signal a stale-branch-head on UpdateFile —
// the same condition GitLab reports as 409. Mapping 422 to KindConflict
// keeps stale-branch conflicts on exit code 5 across all platforms.
func ClassifyStatus(status int) Kind {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return KindAuth
	case status == http.StatusNotFound:
		return KindNotFound
	case status == http.StatusConflict, status == http.StatusUnprocessableEntity:
		return KindConflict
	case status == http.StatusTooManyRequests || status >= 500:
		return KindTransient
	case status >= 400:
		return KindConfig
	default:
		return KindUnknown
	}
}

// As extracts a *Error from an error chain, returning nil if none is found.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// AuthHint returns an operator-friendly diagnostic for the given
// platform + status code. It's used by per-platform classifiers as
// the default fallback for KindAuth errors when no platform-specific
// body parsing is available (i.e. GitLab and Gitea/Forgejo, whose
// response bodies don't carry a reliable token-state signal).
//
// GitHub has its own richer hint logic and calls SetAuthHint
// directly instead of this helper.
//
// Pass platform as the lowercase, user-facing name ("gitlab",
// "github", "gitea/forgejo").
func AuthHint(platform string, status int) string {
	switch status {
	case http.StatusUnauthorized:
		return fmt.Sprintf(
			"token rejected by %s (401 Unauthorized). The token may be expired, revoked, or invalid; verify --api-token is correct and active. "+
				"GitLab PATs are managed at https://gitlab.com/-/user_settings/personal_access_tokens",
			platform,
		)
	case http.StatusForbidden:
		return fmt.Sprintf(
			"token rejected by %s (403 Forbidden). The token is valid but lacks the access required for this operation "+
				"(typically the `api` scope and write access to the target repository)",
			platform,
		)
	default:
		return ""
	}
}
