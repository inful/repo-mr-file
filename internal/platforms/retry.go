package platforms

import (
	"context"
	"io"
	"log/slog"
	"math/rand"
	"time"
)

// RetryConfig configures the retry policy applied by WithRetry.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts including the first.
	// 1 means "do not retry"; 3 means "up to 3 attempts".
	MaxAttempts int
	// InitialBackoff is the wait before the second attempt; subsequent waits
	// double this value (exponential).
	InitialBackoff time.Duration
	// Jitter is the ±fractional jitter applied to each backoff (0 = none,
	// 0.2 = ±20%). Defaults to 0.2 when zero.
	Jitter float64
	// Logger receives Debug-level "retrying" messages; nil disables logging.
	Logger *slog.Logger
	// Rand is used for jitter. nil falls back to a package-private source.
	Rand *rand.Rand
}

// WithRetry wraps a Client so every method retries KindTransient errors up
// to cfg.MaxAttempts total attempts. Other kinds fail immediately. Defaults:
// MaxAttempts=1 (no retry) when <=0; Jitter=0.2 when 0.
func WithRetry(inner Client, cfg RetryConfig) Client {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	if cfg.Jitter == 0 {
		cfg.Jitter = 0.2
	}
	return &retryClient{inner: inner, cfg: cfg}
}

type retryClient struct {
	inner Client
	cfg   RetryConfig
}

func (r *retryClient) GetProject(ctx context.Context, repoPath string) (*Project, error) {
	return retryDo(ctx, r.cfg, "GetProject", func(ctx context.Context) (*Project, error) {
		return r.inner.GetProject(ctx, repoPath)
	})
}

func (r *retryClient) GetBranch(ctx context.Context, repoPath, branch string) (bool, error) {
	return retryDo(ctx, r.cfg, "GetBranch", func(ctx context.Context) (bool, error) {
		return r.inner.GetBranch(ctx, repoPath, branch)
	})
}

func (r *retryClient) CreateBranch(ctx context.Context, repoPath, newBranch, startBranch string) error {
	_, err := retryDo(ctx, r.cfg, "CreateBranch", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.inner.CreateBranch(ctx, repoPath, newBranch, startBranch)
	})
	return err
}

func (r *retryClient) GetFile(ctx context.Context, repoPath, filePath, ref string) (*File, error) {
	return retryDo(ctx, r.cfg, "GetFile", func(ctx context.Context) (*File, error) {
		return r.inner.GetFile(ctx, repoPath, filePath, ref)
	})
}

func (r *retryClient) CreateFile(ctx context.Context, repoPath, branch, filePath, startBranch, commitMsg string, content io.Reader) error {
	_, err := retryDo(ctx, r.cfg, "CreateFile", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.inner.CreateFile(ctx, repoPath, branch, filePath, startBranch, commitMsg, content)
	})
	return err
}

func (r *retryClient) UpdateFile(ctx context.Context, repoPath, branch, filePath, startBranch, commitMsg, lastCommitID string, content io.Reader) error {
	_, err := retryDo(ctx, r.cfg, "UpdateFile", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.inner.UpdateFile(ctx, repoPath, branch, filePath, startBranch, commitMsg, lastCommitID, content)
	})
	return err
}

func (r *retryClient) ListOpenMR(ctx context.Context, repoPath, sourceBranch, targetBranch string) (*MergeRequest, error) {
	return retryDo(ctx, r.cfg, "ListOpenMR", func(ctx context.Context) (*MergeRequest, error) {
		return r.inner.ListOpenMR(ctx, repoPath, sourceBranch, targetBranch)
	})
}

func (r *retryClient) CreateMR(ctx context.Context, repoPath string, in CreateMRInput) (*MergeRequest, error) {
	return retryDo(ctx, r.cfg, "CreateMR", func(ctx context.Context) (*MergeRequest, error) {
		return r.inner.CreateMR(ctx, repoPath, in)
	})
}

// retryDo executes fn with retry on KindTransient errors. It returns the
// final value and error. The function is generic so it can wrap any method
// without per-method boilerplate.
func retryDo[T any](ctx context.Context, cfg RetryConfig, op string, fn func(context.Context) (T, error)) (T, error) {
	var zero T

	var lastErr error
	delay := cfg.InitialBackoff
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		e := As(err)
		if e == nil || e.Kind != KindTransient {
			return zero, err
		}
		lastErr = err
		if attempt == cfg.MaxAttempts {
			break
		}
		// Prefer the server-supplied Retry-After hint over our backoff
		// when present — servers often use it to communicate quota
		// resets that we cannot otherwise know. Skip jitter on a hint
		// since the server has stated a specific moment.
		actualDelay := applyJitter(delay, cfg.Jitter, cfg.Rand)
		if e.RetryAfter > 0 {
			actualDelay = e.RetryAfter // already capped at MaxRetryAfter
		}
		if cfg.Logger != nil {
			cfg.Logger.DebugContext(ctx, "retrying platform request",
				"op", op, "attempt", attempt, "delay", actualDelay, "err", err.Error())
		}
		select {
		case <-time.After(actualDelay):
		case <-ctx.Done():
			return zero, ctx.Err()
		}
		delay *= 2
	}
	return zero, lastErr
}

// applyJitter multiplies d by (1 + jitter*rand[-1,1]). Pure helper.
func applyJitter(d time.Duration, jitter float64, r *rand.Rand) time.Duration {
	if jitter <= 0 {
		return d
	}
	var f float64
	if r != nil {
		f = r.Float64()
	} else {
		// Use package-private RNG seeded lazily; mostly a test/dev fallback.
		f = defaultRandFloat()
	}
	factor := 1.0 + (f*2-1)*jitter
	return time.Duration(float64(d) * factor)
}
