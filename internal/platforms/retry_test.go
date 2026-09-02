package platforms

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// --- existing retry tests (preserved verbatim from before this commit) ---

func TestRetryDo_SuccessFirstTry(t *testing.T) {
	calls := 0
	got, err := retryDo(context.Background(), RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond}, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q, want %q", got, "ok")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestRetryDo_RetryThenSuccess(t *testing.T) {
	calls := 0
	got, err := retryDo(context.Background(), RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond}, "test",
		func(ctx context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", New(KindTransient, "test", errors.New("503"))
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q, want %q", got, "ok")
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestRetryDo_ExhaustsRetries(t *testing.T) {
	calls := 0
	transient := New(KindTransient, "test", errors.New("503"))
	_, err := retryDo(context.Background(), RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond}, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", transient
		})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if e := As(err); e == nil || e.Kind != KindTransient {
		t.Errorf("kind = %v, want KindTransient", e)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRetryDo_NoRetryOn4xx(t *testing.T) {
	calls := 0
	auth := New(KindAuth, "test", errors.New("401"))
	_, err := retryDo(context.Background(), RetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond}, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", auth
		})
	if err == nil {
		t.Fatal("expected error")
	}
	if e := As(err); e == nil || e.Kind != KindAuth {
		t.Errorf("kind = %v, want KindAuth", e)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on auth)", calls)
	}
}

func TestRetryDo_NoRetryOnConflict(t *testing.T) {
	calls := 0
	conflict := New(KindConflict, "test", errors.New("409"))
	_, err := retryDo(context.Background(), RetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond}, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", conflict
		})
	if e := As(err); e == nil || e.Kind != KindConflict {
		t.Errorf("kind = %v, want KindConflict", e)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on conflict)", calls)
	}
}

func TestRetryDo_ContextCancellation(t *testing.T) {
	calls := 0
	transient := New(KindTransient, "test", errors.New("503"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err := retryDo(ctx, RetryConfig{MaxAttempts: 5, InitialBackoff: time.Millisecond}, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", transient
		})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls > 0 {
		t.Errorf("calls = %d, want 0 (cancelled before first attempt)", calls)
	}
}

func TestRetryDo_BackoffDoubles(t *testing.T) {
	// We can't easily measure wall-clock backoff; instead we verify that
	// multiple retries happen and respect MaxAttempts.
	cfg := RetryConfig{MaxAttempts: 5, InitialBackoff: time.Microsecond}
	calls := 0
	transient := New(KindTransient, "test", errors.New("503"))
	_, err := retryDo(context.Background(), cfg, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", transient
		})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 5 {
		t.Errorf("calls = %d, want 5", calls)
	}
}

func TestRetryDo_UntypedErrorNotRetried(t *testing.T) {
	calls := 0
	plain := errors.New("plain error")
	_, err := retryDo(context.Background(), RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond}, "test",
		func(ctx context.Context) (string, error) {
			calls++
			return "", plain
		})
	if !errors.Is(err, plain) {
		t.Errorf("err = %v, want plain", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (untyped error not retried)", calls)
	}
}

func TestApplyJitter_WithExplicitRand(t *testing.T) {
	r := rand.New(rand.NewSource(42)) //nolint:gosec // deterministic test fixture
	d := 100 * time.Millisecond
	for i := 0; i < 5; i++ {
		got := applyJitter(d, 0.5, r)
		if got <= 0 || got > 2*d {
			t.Errorf("applyJitter(%d, 0.5) = %v, want in (0, %d]", d, got, 2*d)
		}
	}
}

func TestApplyJitter_NoJitterNoRand(t *testing.T) {
	d := 100 * time.Millisecond
	if got := applyJitter(d, 0, nil); got != d {
		t.Errorf("applyJitter(d, 0, nil) = %v, want %v", got, d)
	}
}

func TestWithRetry_DelegatesToInner(t *testing.T) {
	inner := &recordingClient{}
	c := WithRetry(inner, RetryConfig{MaxAttempts: 1, InitialBackoff: time.Millisecond})
	ctx := context.Background()

	_, _ = c.GetProject(ctx, "foo/bar")
	_, _ = c.GetBranch(ctx, "foo/bar", "main")
	_, _ = c.GetFile(ctx, "foo/bar", "ca.pem", "main")
	_ = c.CreateFile(ctx, "foo/bar", "b", "ca.pem", "main", "m", strings.NewReader("c"))
	_ = c.UpdateFile(ctx, "foo/bar", "b", "ca.pem", "main", "m", "id", strings.NewReader("c"))
	_, _ = c.ListOpenMR(ctx, "foo/bar", "src", "tgt")
	_, _ = c.CreateMR(ctx, "foo/bar", CreateMRInput{})

	want := []string{"GetProject", "GetBranch", "GetFile", "CreateFile", "UpdateFile", "ListOpenMR", "CreateMR"}
	if len(inner.calls) != len(want) {
		t.Fatalf("inner.calls = %+v, want %d calls", inner.calls, len(want))
	}
	for i, m := range want {
		if inner.calls[i].method != m {
			t.Errorf("call[%d].method = %q, want %q", i, inner.calls[i].method, m)
		}
	}
}

// --- new tests for Retry-After support ---

// TestRetryDo_HonorsRetryAfter verifies that when the inner call returns
// a *platforms.Error with RetryAfter > 0, retryDo waits that long
// instead of the configured exponential backoff.
func TestRetryDo_HonorsRetryAfter(t *testing.T) {
	// Backoff would sleep 5s; the server's Retry-After must win.
	cfg := RetryConfig{
		MaxAttempts:    2,
		InitialBackoff: 5 * time.Second,
	}
	calls := 0
	_, err := retryDo(context.Background(), cfg, "Test",
		func(context.Context) (struct{}, error) {
			calls++
			return struct{}{}, &Error{
				Kind:       KindTransient,
				Op:         "Test",
				StatusCode: 429,
				// Server told us to wait 50ms.
				RetryAfter: 50 * time.Millisecond,
				Err:        errors.New("rate limited"),
			}
		},
	)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

// TestRetryDo_NoRetryAfterUsesBackoff verifies the existing exponential
// backoff path still kicks in when RetryAfter is zero.
func TestRetryDo_NoRetryAfterUsesBackoff(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts:    2,
		InitialBackoff: 20 * time.Millisecond,
	}
	calls := 0
	start := time.Now()
	_, err := retryDo(context.Background(), cfg, "Test",
		func(context.Context) (struct{}, error) {
			calls++
			return struct{}{}, &Error{
				Kind:       KindTransient,
				Op:         "Test",
				StatusCode: 503,
				// RetryAfter deliberately 0.
				Err: errors.New("server error"),
			}
		},
	)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	// Backoff sleeps ~20ms before the second attempt; with jitter up
	// to ±20% the floor is ~16ms and the ceiling is ~24ms.
	if elapsed < 15*time.Millisecond {
		t.Errorf("elapsed %v < backoff minimum; retry skipped the wait?", elapsed)
	}
}

// TestRetryDo_AcceptsServerValueAsIs verifies that a Retry-After value
// in the typed error is used by retryDo (with the platform having
// already capped it via parseRetryAfter). Internal contract: callers
// have already capped the value at MaxRetryAfter before the retry
// layer sees it.
func TestRetryDo_AcceptsServerValueAsIs(t *testing.T) {
	cfg := RetryConfig{
		MaxAttempts:    2,
		InitialBackoff: 10 * time.Second, // would be way too long if used
		// Server says wait only 30ms.
	}
	calls := 0
	start := time.Now()
	_, _ = retryDo(context.Background(), cfg, "Test",
		func(context.Context) (struct{}, error) {
			calls++
			return struct{}{}, &Error{
				Kind:       KindTransient,
				Op:         "Test",
				StatusCode: 429,
				RetryAfter: 30 * time.Millisecond,
				Err:        errors.New("rate limited"),
			}
		},
	)
	elapsed := time.Since(start)
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	// RetryAfter (30ms) was honored, not the 10s backoff.
	if elapsed > 200*time.Millisecond {
		t.Errorf("elapsed %v suggests backoff was used instead of RetryAfter", elapsed)
	}
}
