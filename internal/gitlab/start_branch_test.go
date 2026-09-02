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

// TestOfficialClient_CreateFile_SendsStartBranch is RED until
// official.go populates StartBranch on the CreateFile options. The
// bundler relies on GitLab's implicit branch creation on POST
// /repository/files, which only fires when start_branch is set;
// without it, GitLab returns HTTP 400 "You can only create or edit
// files when you are on a branch".
func TestOfficialClient_CreateFile_SendsStartBranch(t *testing.T) {
	var seenBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody.Store(string(body))
		writeJSON(t, w, http.StatusCreated, map[string]any{"file_path": "ca.pem"})
	}))
	defer srv.Close()

	c := NewOfficialClient(srv.URL+"/api/v4", "test-token")
	if err := c.CreateFile(context.Background(), "foo/bar", "new-branch", "ca.pem", "main", "msg", strings.NewReader("hello")); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	body := seenBody.Load().(string)
	if !strings.Contains(body, `"branch":"new-branch"`) {
		t.Errorf("body missing branch: %s", body)
	}
	if !strings.Contains(body, `"start_branch":"main"`) {
		t.Errorf("body missing start_branch (GitLab requires this to auto-create the branch on POST file): %s", body)
	}
}
