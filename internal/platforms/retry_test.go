package platforms

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

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
	_ = c.CreateFile(ctx, "foo/bar", "b", "ca.pem", "m", strings.NewReader("c"))
	_ = c.UpdateFile(ctx, "foo/bar", "b", "ca.pem", "m", "id", strings.NewReader("c"))
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
