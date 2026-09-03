package bundler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inful/repo-mr-file/internal/gitlab"
	"github.com/inful/repo-mr-file/internal/platforms"
)

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

// TestRun_BranchExists_NoStartBranchOnCreateFile is the regression
// test for the v0.9.7 bug where the bundler passed
// startBranch=branchName to CreateFile when the branch already
// existed. GitLab's Files::CreateService then invoked
// Branches::CreateService a second time and failed with HTTP 400
// "A branch called 'X' already exists", surfacing as a KindConfig
// exit (code 2). The fix: when the branch exists, no start_branch
// is sent to GitLab at all.
func TestRun_BranchExists_NoStartBranchOnCreateFile(t *testing.T) {
	mock := newMockGitLab(t)
	mock.branchExists = true
	// File does not exist on the branch (fileStatus=404 by default),
	// so CreateFile (POST) is the chosen write path.
	deps := stubDeps(t, mock, []byte("bundle"), defaultConfig())

	_, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := mock.createFileCalls.Load(); got != 1 {
		t.Errorf("CreateFile calls = %d, want 1", got)
	}
	startRef, _ := mock.createFileStartRef.Load().(string)
	if startRef != "" {
		t.Errorf("CreateFile start_branch = %q, want empty (branch already existed)", startRef)
	}
}

// TestRun_BranchMissing_NoStartBranchOnCreateFile locks in the
// v0.9.8 design: CreateFile never receives a start_branch, regardless
// of whether the branch existed or was just created. The bundler's
// CreateBranch call (step 3) ensures the branch exists, and passing
// start_branch to GitLab's CreateFile would re-trigger
// Branches::CreateService and fail with HTTP 400.
func TestRun_BranchMissing_NoStartBranchOnCreateFile(t *testing.T) {
	mock := newMockGitLab(t)
	mock.branchExists = false
	deps := stubDeps(t, mock, []byte("bundle"), defaultConfig())

	_, err := Run(context.Background(), deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := mock.createFileCalls.Load(); got != 1 {
		t.Errorf("CreateFile calls = %d, want 1", got)
	}
	startRef, _ := mock.createFileStartRef.Load().(string)
	if startRef != "" {
		t.Errorf("CreateFile start_branch = %q, want empty (bundler always pre-creates branch)", startRef)
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

// TestRun_CreateFile_FreshBranchNoStartBranch_NoBranchExistsError is the
// end-to-end regression test for the v0.9.8 bug. Real-world reproduction:
//
//  1. Bundler calls GetBranch → false.
//  2. Bundler calls CreateBranch → succeeds.
//  3. Bundler calls CreateFile with start_branch=main. GitLab's
//     Files::CreateService invokes Branches::CreateService again
//     and returns HTTP 400 "A branch called 'X' already exists"
//     even though the branch was just created.
//
// The fix: never send start_branch to CreateFile. This test fails
// any CreateFile that contains "start_branch" in its request body.
//
// The mock uses a real httptest server + the actual GitLab Go
// client to exercise the end-to-end flow.
func TestRun_CreateFile_FreshBranchNoStartBranch_NoBranchExistsError(t *testing.T) {
	var (
		mu                  sync.Mutex
		createFileBodies    []string
		sawStartBranchInAny bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/projects/foo/bar"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "default_branch": "main"})
		case r.Method == http.MethodGet && strings.Contains(path, "/repository/branches/update-v1"):
			// Branch missing initially.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 Branch Not Found"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/repository/branches"):
			// CreateBranch succeeds.
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"update-v1"}`))
		case r.Method == http.MethodGet && strings.Contains(path, "/repository/files/"):
			// File doesn't exist on the new branch.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 File Not Found"}`))
		case r.Method == http.MethodPost && strings.Contains(path, "/repository/files/"):
			// CreateFile: must NOT contain start_branch.
			mu.Lock()
			createFileBodies = append(createFileBodies, string(body))
			if strings.Contains(string(body), "start_branch") {
				sawStartBranchInAny = true
			}
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"file_path":"ca.pem"}`))
		case r.Method == http.MethodGet && strings.Contains(path, "/merge_requests"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && strings.Contains(path, "/merge_requests"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"iid":7,"web_url":"https://example/foo/bar/-/merge_requests/7"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"unexpected: ` + r.Method + " " + path + `"}`))
		}
	}))
	defer srv.Close()

	cfg := defaultConfig()
	cfg.Repo = "foo/bar"
	cfg.BranchName = "update-v1"
	cfg.TargetPath = "ca.pem"
	cfg.TargetBranch = "main"

	oc := gitlab.NewOfficialClient(srv.URL+"/api/v4", "test-token")
	deps := Deps{
		Client: platforms.WithRetry(oc, platforms.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Microsecond}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: cfg,
		Source: []byte("new content"),
	}

	if _, err := Run(context.Background(), deps); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(createFileBodies) != 1 {
		t.Errorf("CreateFile calls = %d, want 1", len(createFileBodies))
	}
	if sawStartBranchInAny {
		t.Errorf("CreateFile request body contained start_branch; this re-triggers GitLab's Branches::CreateService and produces the 'A branch called X already exists' 400 error. bodies: %v", createFileBodies)
	}
}
