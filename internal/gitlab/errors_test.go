package gitlab

import (
	"errors"
	"net/http"
	"testing"
)

func TestClassifyStatus(t *testing.T) {
	cases := []struct {
		status int
		want   Kind
	}{
		{http.StatusOK, KindUnknown},
		{http.StatusMovedPermanently, KindUnknown},
		{http.StatusBadRequest, KindConfig},
		{http.StatusUnauthorized, KindAuth},
		{http.StatusForbidden, KindAuth},
		{http.StatusNotFound, KindNotFound},
		{http.StatusConflict, KindConflict},
		{http.StatusUnprocessableEntity, KindConfig},
		{http.StatusTooManyRequests, KindTransient},
		{http.StatusInternalServerError, KindTransient},
		{http.StatusBadGateway, KindTransient},
		{http.StatusServiceUnavailable, KindTransient},
		{http.StatusGatewayTimeout, KindTransient},
	}
	for _, tc := range cases {
		if got := ClassifyStatus(tc.status); got != tc.want {
			t.Errorf("ClassifyStatus(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestError_ErrorIncludesOp(t *testing.T) {
	e := New(KindAuth, "GetProject", errors.New("401 Unauthorized"))
	got := e.Error()
	want := "GetProject: 401 Unauthorized"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestError_ErrorWithoutOp(t *testing.T) {
	e := New(KindAuth, "", errors.New("401 Unauthorized"))
	got := e.Error()
	want := "401 Unauthorized"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestError_Unwrap(t *testing.T) {
	inner := errors.New("inner error")
	e := New(KindAuth, "GetProject", inner)
	if !errors.Is(errors.Unwrap(e), inner) {
		t.Error("Unwrap did not return the inner error")
	}
}

func TestError_IsMatchesKind(t *testing.T) {
	e := New(KindAuth, "GetProject", errors.New("401"))
	target := &Error{Kind: KindAuth}
	if !errors.Is(e, target) {
		t.Error("errors.Is should match same Kind")
	}
	other := &Error{Kind: KindNotFound}
	if errors.Is(e, other) {
		t.Error("errors.Is should not match different Kind")
	}
}

func TestError_IsDoesNotMatchUntyped(t *testing.T) {
	e := New(KindAuth, "GetProject", errors.New("401"))
	target := errors.New("401")
	if errors.Is(e, target) {
		t.Error("errors.Is should not match an untyped error")
	}
}

func TestAs(t *testing.T) {
	e := New(KindAuth, "GetProject", errors.New("401"))
	if got := As(e); got != e {
		t.Errorf("As(e) = %v, want %v", got, e)
	}

	wrapped := errors.Join(errors.New("wrapped"), e)
	if got := As(wrapped); got != e {
		t.Errorf("As on errors.Join should return the *Error; got %v", got)
	}

	other := errors.New("plain")
	if got := As(other); got != nil {
		t.Errorf("As(other) = %v, want nil", got)
	}
}

func TestKindString(t *testing.T) {
	cases := map[Kind]string{
		KindUnknown:   "unknown",
		KindConfig:    "config",
		KindAuth:      "auth",
		KindNotFound:  "not-found",
		KindConflict:  "conflict",
		KindTransient: "transient",
		KindInternal:  "internal",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", k, got, want)
		}
	}
}
