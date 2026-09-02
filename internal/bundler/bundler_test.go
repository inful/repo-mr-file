package bundler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inful/repo-mr-file/internal/gitlab"
	"github.com/inful/repo-mr-file/internal/platforms"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// mockGitLab is a programmable httptest server that mimics the GitLab API
// endpoints the bundler uses. Tests configure it by setting fields before
// running the bundler; calls are recorded for assertions.
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
	getProjectCalls   atomic.Int32
	getBranchCalls    atomic.Int32
	getFileCalls      atomic.Int32
	getFileRef        atomic.Value // string
	createFileCalls   atomic.Int32
	createFileContent atomic.Value // []byte
	createBranchCalls atomic.Int32
	createBranchReqs  []struct {
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// stubDeps constructs a Deps pointing at the mock server.
func stubDeps(t *testing.T, mock *mockGitLab, bundle []byte, config Config) Deps {
	t.Helper()
	oc := gitlab.NewOfficialClient(mock.URL()+"/api/v4", "test-token")
	client := platforms.WithRetry(oc, platforms.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Microsecond})
	return Deps{
		Client: client,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config,
		Source: bundle,
		DryRun: false,
	}
}

func defaultConfig() Config {
	return Config{
		Label:         "v1.2.3",
		Repo:          "foo/bar",
		TargetPath:    "ca.pem",
		TargetBranch:  "main",
		BranchName:    "update-v1.2.3",
		CommitMessage: "Update ca.pem to release v1.2.3",
		MRTitle:       "Update ca.pem to release v1.2.3",
		MRDescription: "test description",
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRun_FreshRepoNoFileNoMR_CreatesFileAndMR(t *testing.T) {
	mock := newMockGitLab(t)
	// fileStatus defaults to 404; no MR; branch doesn't exist (default)
	deps := stubDeps(t, mock, []byte("new bundle"), defaultConfig())

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Skipped {
		t.Error("Skipped = true, want false")
	}
	if res.MRURL == "" {
		t.Error("MRURL empty, want non-empty")
	}

	if got := mock.getProjectCalls.Load(); got != 1 {
		t.Errorf("getProjectCalls = %d, want 1", got)
	}
	if got := mock.getBranchCalls.Load(); got != 1 {
		t.Errorf("getBranchCalls = %d, want 1", got)
	}
	if got := mock.getFileCalls.Load(); got != 1 {
		t.Errorf("getFileCalls = %d, want 1", got)
	}
	if got := mock.createFileCalls.Load(); got != 1 {
		t.Errorf("createFileCalls = %d, want 1", got)
	}
	if got := mock.updateFileCalls.Load(); got != 0 {
		t.Errorf("updateFileCalls = %d, want 0", got)
	}
	if got := mock.listOpenMRCalls.Load(); got != 1 {
		t.Errorf("listOpenMRCalls = %d, want 1", got)
	}
	if got := mock.createMRCalls.Load(); got != 1 {
		t.Errorf("createMRCalls = %d, want 1", got)
	}
}

func TestRun_FileExistsBundleDiffers_UpdatesFileAndCreatesMR(t *testing.T) {
	mock := newMockGitLab(t)
	mock.fileStatus = 200
	mock.fileContent = []byte("old bundle")
	mock.fileLastCommitID = "oldcommit"
	deps := stubDeps(t, mock, []byte("new bundle"), defaultConfig())

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Skipped {
		t.Error("Skipped = true, want false")
	}
	if got := mock.createFileCalls.Load(); got != 0 {
		t.Errorf("createFileCalls = %d, want 0", got)
	}
	if got := mock.updateFileCalls.Load(); got != 1 {
		t.Errorf("updateFileCalls = %d, want 1", got)
	}
	gotCID, _ := mock.updateFileLastCID.Load().(string)
	if gotCID != "oldcommit" {
		t.Errorf("updateFileLastCommitID = %q, want oldcommit", gotCID)
	}
}

func TestRun_FileMatchesMRExists_NoOps(t *testing.T) {
	mock := newMockGitLab(t)
	mock.fileStatus = 200
	mock.fileContent = []byte("same bundle")
	mock.fileLastCommitID = "samecommit"
	mock.listOpenMRResp = `[{"iid":99,"web_url":"https://example.com/mr/99"}]`
	deps := stubDeps(t, mock, []byte("same bundle"), defaultConfig())

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Skipped {
		t.Error("Skipped = false, want true")
	}
	if res.MRURL != "https://example.com/mr/99" {
		t.Errorf("MRURL = %q, want existing MR url", res.MRURL)
	}
	if got := mock.createFileCalls.Load() + mock.updateFileCalls.Load(); got != 0 {
		t.Errorf("file writes = %d, want 0", got)
	}
	if got := mock.createMRCalls.Load(); got != 0 {
		t.Errorf("createMRCalls = %d, want 0", got)
	}
}

func TestRun_FileMatchesSourceEqualsTarget_NoOps(t *testing.T) {
	mock := newMockGitLab(t)
	mock.fileStatus = 200
	mock.fileContent = []byte("same bundle")
	mock.fileLastCommitID = "cid"
	cfg := defaultConfig()
	cfg.BranchName = cfg.TargetBranch // source == target
	deps := stubDeps(t, mock, []byte("same bundle"), cfg)

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Skipped {
		t.Error("Skipped = false, want true")
	}
	if got := mock.createMRCalls.Load(); got != 0 {
		t.Errorf("createMRCalls = %d, want 0", got)
	}
}

func TestRun_FileMatchesNoMR_CreatesMRWithoutFileWrite(t *testing.T) {
	mock := newMockGitLab(t)
	mock.branchExists = true // source branch differs from target
	mock.fileStatus = 200
	mock.fileContent = []byte("same bundle")
	mock.fileLastCommitID = "cid"
	deps := stubDeps(t, mock, []byte("same bundle"), defaultConfig())

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Skipped {
		t.Error("Skipped = true, want false")
	}
	if got := mock.createFileCalls.Load() + mock.updateFileCalls.Load(); got != 0 {
		t.Errorf("file writes = %d, want 0", got)
	}
	if got := mock.createMRCalls.Load(); got != 1 {
		t.Errorf("createMRCalls = %d, want 1", got)
	}
}

func TestRun_BranchExists_UsedAsFileRef(t *testing.T) {
	mock := newMockGitLab(t)
	mock.branchExists = true
	// Branch exists → GetFile should be called with BranchName as ref.
	deps := stubDeps(t, mock, []byte("bundle"), defaultConfig())

	_, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	gotRef, _ := mock.getFileRef.Load().(string)
	wantRef := deps.Config.BranchName
	if gotRef != wantRef {
		t.Errorf("GetFile ref = %q, want %q", gotRef, wantRef)
	}
}

func TestRun_BranchMissing_UsesTargetAsFileRef(t *testing.T) {
	mock := newMockGitLab(t)
	mock.branchExists = false
	deps := stubDeps(t, mock, []byte("bundle"), defaultConfig())

	_, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	gotRef, _ := mock.getFileRef.Load().(string)
	wantRef := deps.Config.TargetBranch
	if gotRef != wantRef {
		t.Errorf("GetFile ref = %q, want %q", gotRef, wantRef)
	}
}

func TestRun_UpdateFileConflict_PropagatesError(t *testing.T) {
	mock := newMockGitLab(t)
	mock.fileStatus = 200
	mock.fileContent = []byte("old")
	mock.fileLastCommitID = "old"
	mock.updateFileStatus = http.StatusConflict
	deps := stubDeps(t, mock, []byte("new"), defaultConfig())

	_, err := Run(context.Background(), deps)
	if err == nil {
		t.Fatal("expected conflict error")
	}
	e := platforms.As(err)
	if e == nil || e.Kind != platforms.KindConflict {
		t.Errorf("Kind = %v, want KindConflict (err = %v)", e, err)
	}
}

func TestRun_CreateMR422_ReusesConcurrentMR(t *testing.T) {
	mock := newMockGitLab(t)
	mock.createMRStatus = http.StatusUnprocessableEntity
	// First ListOpenMR: no MR. Second (after 422): the concurrent MR.
	mock.listOpenMRFn = func(callNum int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if callNum == 1 {
			_, _ = w.Write([]byte("[]"))
			return
		}
		_, _ = w.Write([]byte(`[{"iid":88,"web_url":"https://example.com/mr/88"}]`))
	}
	deps := stubDeps(t, mock, []byte("bundle"), defaultConfig())

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MRURL != "https://example.com/mr/88" {
		t.Errorf("MRURL = %q, want concurrent MR url", res.MRURL)
	}
	if got := mock.createMRCalls.Load(); got != 1 {
		t.Errorf("createMRCalls = %d, want 1", got)
	}
	if got := mock.listOpenMRCalls.Load(); got != 2 {
		t.Errorf("listOpenMRCalls = %d, want 2 (initial + retry)", got)
	}
}

func TestRun_DryRun_NoNetworkCalls(t *testing.T) {
	mock := newMockGitLab(t)
	deps := stubDeps(t, mock, []byte("bundle"), defaultConfig())
	deps.DryRun = true
	deps.Client = platforms.NewDryRunClient()

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.DryRun {
		t.Error("DryRun = false, want true")
	}
}

func TestRun_DryRun_NilLoggerDoesNotPanic(t *testing.T) {
	mock := newMockGitLab(t)
	deps := stubDeps(t, mock, []byte("bundle"), defaultConfig())
	deps.DryRun = true
	deps.Logger = nil // explicitly nil — should not panic
	deps.Client = platforms.NewDryRunClient()

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.DryRun {
		t.Error("DryRun = false, want true")
	}
}

func TestRun_TargetBranchFromProjectDefault(t *testing.T) {
	mock := newMockGitLab(t)
	mock.projectDefaultBranch = "develop"
	cfg := defaultConfig()
	cfg.TargetBranch = "" // let bundler pick from project
	deps := stubDeps(t, mock, []byte("bundle"), cfg)

	res, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mock.createMRReqBodies) != 1 {
		t.Fatalf("createMRReqBodies = %d, want 1", len(mock.createMRReqBodies))
	}
	if mock.createMRReqBodies[0].TargetBranch != "develop" {
		t.Errorf("MR target_branch = %q, want develop", mock.createMRReqBodies[0].TargetBranch)
	}
	if res.MRURL == "" {
		t.Error("MRURL empty")
	}
}

func TestRun_NoDefaultBranch_ReturnsError(t *testing.T) {
	mock := newMockGitLab(t)
	mock.projectDefaultBranch = ""
	cfg := defaultConfig()
	cfg.TargetBranch = "" // both empty
	deps := stubDeps(t, mock, []byte("bundle"), cfg)

	_, err := Run(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error for missing default branch")
	}
	if e := platforms.As(err); e == nil || e.Kind != platforms.KindConfig {
		t.Errorf("Kind = %v, want KindConfig (err = %v)", e, err)
	}
}

func TestRun_LoggerLogsBashMirroringLines(t *testing.T) {
	mock := newMockGitLab(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	oc := gitlab.NewOfficialClient(mock.URL()+"/api/v4", "test-token")
	deps := Deps{
		Client: platforms.WithRetry(oc, platforms.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Microsecond}),
		Logger: logger,
		Config: defaultConfig(),
		Source: []byte("new"),
	}

	if _, err := Run(context.Background(), deps); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()
	wantSubstrings := []string{
		"Getting project info for foo/bar",
		"Found project ID: 42",
		"Using target branch: main",
		"Checking if branch update-v1.2.3 exists",
		"Branch does not exist, will create from main",
		"Creating ca.pem in foo/bar",
		"File POST completed in branch",
		"Creating merge request",
		"Merge request created",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q\noutput:\n%s", want, out)
		}
	}
}

// keep imports referenced if a future edit removes some uses
var _ = os.Getenv
var _ slog.Handler = (*slog.JSONHandler)(nil)
