package bundler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// mockGitLab is a programmable httptest server that mimics the GitLab API
// endpoints the bundler uses. Tests configure it by setting fields before
// running the bundler; calls are recorded for assertions.
//
// Lives in its own file so the test cases in bundler_test.go aren't
// drowning in 100+ lines of routing code.
type mockGitLab struct {
	srv *httptest.Server

	// Configurable behavior.
	projectID            int
	projectDefaultBranch string
	branchExists         bool
	fileStatus           int    // HTTP status for GetFile (200 or 404)
	fileContent          []byte // returned (base64-encoded) on GetFile 200
	fileLastCommitID     string
	listOpenMRStatus     int
	listOpenMRResp       string // JSON array body for ListOpenMR
	// listOpenMRFn, if set, overrides the default static response. It
	// receives the call number (1-indexed) and must write the response.
	listOpenMRFn     func(callNum int, w http.ResponseWriter, r *http.Request)
	createFileStatus int
	updateFileStatus int
	createMRStatus   int
	createMRResp     string // JSON body for CreateMR

	// Recorded calls.
	getProjectCalls    atomic.Int32
	getBranchCalls     atomic.Int32
	getFileCalls       atomic.Int32
	getFileRef         atomic.Value // string
	createFileCalls    atomic.Int32
	createFileContent  atomic.Value // []byte
	createFileStartRef atomic.Value // string — start_branch from the request body
	createBranchCalls  atomic.Int32
	createBranchReqs   []struct {
		Branch string `json:"branch"`
		Ref    string `json:"ref"`
	}
	updateFileCalls   atomic.Int32
	updateFileContent atomic.Value // []byte
	updateFileLastCID atomic.Value // string
	listOpenMRCalls   atomic.Int32
	createMRCalls     atomic.Int32
	createMRReqBodies []createMRReq
}

type createMRReq struct {
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
	Description  string `json:"description"`
}

func newMockGitLab(t *testing.T) *mockGitLab {
	t.Helper()
	m := &mockGitLab{
		projectID:            42,
		projectDefaultBranch: "main",
		branchExists:         false,
		fileStatus:           404,
		listOpenMRStatus:     200,
		listOpenMRResp:       "[]",
		createFileStatus:     201,
		updateFileStatus:     200,
		createMRStatus:       201,
		createMRResp:         `{"iid":7,"web_url":"https://gitlab.example.com/foo/bar/-/merge_requests/7"}`,
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockGitLab) URL() string { return m.srv.URL }

func (m *mockGitLab) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.Contains(path, "/projects/") && !strings.Contains(path, "/repository/") && !strings.Contains(path, "/merge_requests"):
		m.getProjectCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":             m.projectID,
			"default_branch": m.projectDefaultBranch,
		})

	case r.Method == http.MethodGet && strings.Contains(path, "/repository/branches/"):
		m.getBranchCalls.Add(1)
		if !m.branchExists {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Branch Not Found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": "branch"})

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/repository/branches"):
		// CreateBranch: respond 201 with a minimal branch payload.
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Branch string `json:"branch"`
			Ref    string `json:"ref"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Branch != "" && req.Ref != "" {
			m.createBranchCalls.Add(1)
			m.createBranchReqs = append(m.createBranchReqs, req)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"name": req.Branch})

	case r.Method == http.MethodGet && strings.Contains(path, "/repository/files/"):
		m.getFileCalls.Add(1)
		m.getFileRef.Store(r.URL.Query().Get("ref"))
		if m.fileStatus != http.StatusOK {
			writeJSON(w, m.fileStatus, map[string]any{"message": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"file_path":      "ca.pem",
			"content":        base64.StdEncoding.EncodeToString(m.fileContent),
			"encoding":       "base64",
			"last_commit_id": m.fileLastCommitID,
			"ref":            r.URL.Query().Get("ref"),
		})

	case r.Method == http.MethodPost && strings.Contains(path, "/repository/files/"):
		m.createFileCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		m.createFileContent.Store(body)
		var payload struct {
			StartBranch string `json:"start_branch"`
		}
		_ = json.Unmarshal(body, &payload)
		m.createFileStartRef.Store(payload.StartBranch)
		writeJSON(w, m.createFileStatus, map[string]any{"file_path": "ca.pem"})

	case r.Method == http.MethodPut && strings.Contains(path, "/repository/files/"):
		m.updateFileCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		m.updateFileContent.Store(body)
		// Capture last_commit_id from request body if present.
		var payload struct {
			LastCommitID string `json:"last_commit_id"`
		}
		_ = json.Unmarshal(body, &payload)
		m.updateFileLastCID.Store(payload.LastCommitID)
		writeJSON(w, m.updateFileStatus, map[string]any{"file_path": "ca.pem"})

	case r.Method == http.MethodGet && strings.Contains(path, "/merge_requests"):
		m.listOpenMRCalls.Add(1)
		if m.listOpenMRFn != nil {
			m.listOpenMRFn(int(m.listOpenMRCalls.Load()), w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.listOpenMRStatus)
		_, _ = w.Write([]byte(m.listOpenMRResp))

	case r.Method == http.MethodPost && strings.Contains(path, "/merge_requests"):
		m.createMRCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req createMRReq
		_ = json.Unmarshal(body, &req)
		m.createMRReqBodies = append(m.createMRReqBodies, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.createMRStatus)
		_, _ = w.Write([]byte(m.createMRResp))

	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "unexpected: " + r.Method + " " + path})
	}
}

// writeJSON is the test-server-side helper used only by mockGitLab.
// (The per-platform test files have their own t.Fatalf-on-encode-failure
// variant; the bundler mock uses the silent variant because the encode
// error case is unreachable — the only body shape used is map[string]any,
// which json.Encoder handles without error.)
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
