package platforms

import (
	"net/http"
	"strconv"
	"time"
)

// MaxRetryAfter caps server-supplied Retry-After values at a value
// reasonable for a CLI tool's wall-clock budget. A misbehaving server
// that asks us to wait hours is treated as MaxRetryAfter (the upper
// bound for sane rate-limit responses from any platform we target).
const MaxRetryAfter = 60 * time.Second

// parseRetryAfter extracts a Retry-After duration from an HTTP header.
// Supports both the delta-seconds form ("Retry-After: 30") and the
// HTTP-date form ("Retry-After: Wed, 21 Oct 2026 07:28:00 GMT")
// defined in RFC 7231 §7.1.3. The result is capped at MaxRetryAfter.
//
// Returns 0 for missing or malformed headers, for negative deltas, and
// for HTTP-dates that have already passed (the server asked us to retry
// at a moment that's already gone — try now).
//
// (Internal — kept in snake_case by package convention. Per-platform
// clients use RetryAfterFromHeader below.)
func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	// Delta-seconds form is the most common.
	if n, err := strconv.Atoi(v); err == nil {
		return capRetryAfter(time.Duration(n) * time.Second)
	}
	// Fall back to HTTP-date.
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return capRetryAfter(d)
		}
		return 0
	}
	return 0
}

// RetryAfterFromHeader is the public form of parseRetryAfter for use
// by per-platform clients. Platform code calls this when constructing
// a *platforms.Error from an HTTP response so the retry layer can
// honor the server's wait hint.
func RetryAfterFromHeader(h http.Header) time.Duration { return parseRetryAfter(h) }

// capRetryAfter clamps d at the [0, MaxRetryAfter] range. Negative
// values clamp to 0; values above MaxRetryAfter clamp to MaxRetryAfter.
func capRetryAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > MaxRetryAfter {
		return MaxRetryAfter
	}
	return d
}
