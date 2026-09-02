package platforms

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseRetryAfter_DeltaSeconds(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want time.Duration
	}{
		{"empty", "", 0},
		{"zero", "0", 0},
		{"five", "5", 5 * time.Second},
		{"thirty", "30", 30 * time.Second},
		{"sixty", "60", 60 * time.Second},
		{"negative", "-1", 0},
		{"junk", "abc", 0},
		{"above_cap", strconv.Itoa(int(MaxRetryAfter.Seconds()) + 60), MaxRetryAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.val != "" {
				h.Set("Retry-After", tc.val)
			}
			if got := parseRetryAfter(h); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestParseRetryAfter_HTTPDate_Past(t *testing.T) {
	// A date in the past should resolve to zero (server told us to retry
	// at a moment that's already passed — try now).
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(-1*time.Hour).UTC().Format(http.TimeFormat))
	if got := parseRetryAfter(h); got != 0 {
		t.Errorf("parseRetryAfter(past date) = %v, want 0", got)
	}
}

func TestParseRetryAfter_HTTPDate_Future(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(45*time.Second).UTC().Format(http.TimeFormat))
	got := parseRetryAfter(h)
	// Allow ±2s wiggle for test execution time.
	if got < 43*time.Second || got > 47*time.Second {
		t.Errorf("parseRetryAfter(future date) = %v, want ≈45s", got)
	}
}

func TestParseRetryAfter_HTTPDate_OverCap(t *testing.T) {
	// Date 24h in the future should resolve to MaxRetryAfter (capped).
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(24*time.Hour).UTC().Format(http.TimeFormat))
	got := parseRetryAfter(h)
	if got != MaxRetryAfter {
		t.Errorf("parseRetryAfter(24h future date) = %v, want %v (capped)", got, MaxRetryAfter)
	}
}

func TestCapRetryAfter(t *testing.T) {
	if got := capRetryAfter(0); got != 0 {
		t.Errorf("cap(0) = %v, want 0", got)
	}
	if got := capRetryAfter(-5 * time.Second); got != 0 {
		t.Errorf("cap(-5s) = %v, want 0", got)
	}
	if got := capRetryAfter(10 * time.Second); got != 10*time.Second {
		t.Errorf("cap(10s) = %v, want 10s (under cap)", got)
	}
	if got := capRetryAfter(MaxRetryAfter + 1*time.Second); got != MaxRetryAfter {
		t.Errorf("cap(over) = %v, want %v", got, MaxRetryAfter)
	}
}
