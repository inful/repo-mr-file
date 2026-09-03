package gitlab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestOfficialClient_CreateFile_DoesNotSendStartBranch locks in the
// v0.9.8 fix: the CreateFile signature no longer takes a startBranch
// parameter, and the request body must not include a start_branch
// field. Sending it triggers GitLab's Files::CreateService to
// invoke Branches::CreateService a second time, which fails with
// HTTP 400 "A branch called 'X' already exists" — even when the
// bundler just successfully created the branch via CreateBranch.
//
// The bundler ensures the branch exists via CreateBranch before
// calling CreateFile, so no implicit-branch-create is needed (and
// attempting it is harmful).
func TestOfficialClient_CreateFile_DoesNotSendStartBranch(t *testing.T) {
	var seenBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody.Store(string(body))
		writeJSON(t, w, http.StatusCreated, map[string]any{"file_path": "ca.pem"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	if err := c.CreateFile(context.Background(), "foo/bar", "new-branch", "ca.pem", "msg", strings.NewReader("hello")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	body := seenBody.Load().(string)
	if !strings.Contains(body, `"branch":"new-branch"`) {
		t.Errorf("body missing branch: %s", body)
	}
	if strings.Contains(body, "start_branch") {
		t.Errorf("body must not contain start_branch (triggers Branches::CreateService on GitLab): %s", body)
	}
}
