// Package bundler orchestrates the 7-step platform-agnostic workflow that
// creates or updates a file in a GitLab, GitHub, Gitea, or Forgejo repository
// and ensures an open merge/pull request exists for that change.
//
// The public Run entry point accepts a Deps value (Client, Logger, Config,
// Source, DryRun) and returns a Result plus a typed error. All API calls
// go through the Client interface, which is wrapped by WithRetry at the
// caller. The mock httptest server in bundler_test.go exercises every
// branch end-to-end.
package bundler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/inful/repo-mr-file/internal/platforms"
)

// Log message templates. The bash script this binary replaces emitted
// specific echo lines that operators may grep for in CI logs; these
// constants capture those lines verbatim so callers can format them
// with fmt.Sprintf. Keep them stable — operators may grep for them
// in pipeline output.
//
// These used to live in a dedicated internal/logging package; they
// moved here because only the bundler emits them.
const (
	msgGettingProjectInfo = "Getting project info for %s..."
	msgFoundProjectID     = "Found project ID: %d"
	msgUsingTargetBranch  = "Using target branch: %s"
	msgCheckingBranch     = "Checking if branch %s exists..."
	msgBranchExists       = "Branch exists, will update existing branch"
	msgBranchDoesNotExist = "Branch does not exist, will create from %s..."
	msgBundleMatches      = "%s already matches the source bundle"
	msgUpdatingFile       = "Updating %s in %s..."
	msgCreatingFile       = "Creating %s in %s..."
	msgFileUpdated        = "✓ File %s completed in branch %s"
	msgCreatingMR         = "Creating merge request..."
	msgMRCreated          = "✓ Merge request created: %s"
	msgExistingMR         = "✓ Existing MR: %s"
	msgNoUpdateNeeded     = "✓ No update or merge request is needed"
)

// Config is the subset of CLI fields the bundler needs.
type Config struct {
	Label         string
	Repo          string
	TargetPath    string
	TargetBranch  string
	BranchName    string
	CommitMessage string
	MRTitle       string
	MRDescription string
}

// Deps carries the runtime dependencies Run needs.
type Deps struct {
	Client platforms.Client
	Logger *slog.Logger
	Config Config
	Source []byte
	DryRun bool
}

// Result describes the outcome of Run. Skipped is true when no API call
// was needed (bundle already up to date); DryRun is true when the
// --dry-run path was taken (no real API calls).
type Result struct {
	Skipped bool
	DryRun  bool
	MRURL   string
}

// Run executes the workflow.
//
// Steps:
//  1. Determine project (GetProject).
//  2. Resolve target branch (config > project default).
//  3. Resolve source branch (BranchName if it exists; otherwise target).
//  4. Look up an existing open MR for (source, target).
//  5. Read current target file; decide POST vs PUT vs no-write.
//  6. Write the file if needed.
//  7. Short-circuit when the bundle already matches: skip if an MR
//     exists, or if source==target (no MR needed at all). Otherwise
//     create the MR; reuse it if the call fails with 422 because a
//     concurrent run beat us to it.
//
// The same code path drives both live mode and --dry-run: deps.Client
// is the dryRunClient when --dry-run is set, which returns KindNotFound
// for GetFile (forcing the POST path) and a fake WebURL for CreateMR.
// Result.DryRun is populated from deps.DryRun via the deferred
// assignment at the top of Run.
func Run(ctx context.Context, deps Deps) (result Result, err error) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	defer func() {
		// Stamp DryRun on the returned value (including the zero Result
		// returned with an error) so callers can distinguish a dry-run
		// pass from a live pass without checking the input. The error
		// path also gets a DryRun=true stamp if applicable, but no
		// caller reads Result on error — see run() in cmd/repo-mr-file.
		result.DryRun = deps.DryRun
	}()

	// Step 1: project.
	logger.Info(fmt.Sprintf(msgGettingProjectInfo, deps.Config.Repo))
	proj, err := deps.Client.GetProject(ctx, deps.Config.Repo)
	if err != nil {
		return Result{}, err
	}
	logger.Info(fmt.Sprintf(msgFoundProjectID, proj.ID))

	// Step 2: target branch.
	targetBranch := deps.Config.TargetBranch
	if targetBranch == "" {
		targetBranch = proj.DefaultBranch
	}
	if targetBranch == "" {
		return Result{}, platforms.New(platforms.KindConfig, "Run",
			errors.New("project has no default branch and --target-branch was not set"))
	}
	logger.Info(fmt.Sprintf(msgUsingTargetBranch, targetBranch))

	// Step 3: source branch. If our branch doesn't exist yet, each
	// platform's CreateBranch implementation creates it from targetBranch
	// (the project's default). This avoids GitHub's "404 Branch not
	// found" / GitLab's "you can only create files on a branch" errors
	// on populated repos where PUT /contents/branch can't auto-create.
	logger.Info(fmt.Sprintf(msgCheckingBranch, deps.Config.BranchName))
	branchExists, err := deps.Client.GetBranch(ctx, deps.Config.Repo, deps.Config.BranchName)
	if err != nil {
		return Result{}, err
	}
	// fileBranch is the ref we GetFile from — usually the new branch,
	// but the parent's ref when the branch was just created (the new
	// branch inherits the parent's tree, and GetFile against the
	// freshly-created branch can lag behind the parent's API state).
	//
	// parentBranch is the value we pass as CreateFile's startBranch so
	// the platform can auto-create the new branch atomically. It's
	// empty when the branch already exists; passing it as the existing
	// branch's own name would cause GitLab's Files::CreateService to
	// invoke Branches::CreateService a second time and fail with
	// "A branch called 'X' already exists" (HTTP 400).
	//
	// The bug shipped before v0.9.7 because the bundler conflated
	// "the branch we operate on" with "the parent for auto-creation"
	// into a single sourceBranch value.
	var fileBranch, parentBranch string
	if branchExists {
		logger.Info(msgBranchExists)
		fileBranch = deps.Config.BranchName
		parentBranch = "" // no auto-creation needed
	} else {
		logger.Info(fmt.Sprintf(msgBranchDoesNotExist, targetBranch))
		if err := deps.Client.CreateBranch(ctx, deps.Config.Repo, deps.Config.BranchName, targetBranch); err != nil {
			return Result{}, err
		}
		fileBranch = targetBranch // GetFile against parent; new branch inherits it
		parentBranch = targetBranch
	}

	// Step 4: existing MR.
	existingMR, err := deps.Client.ListOpenMR(ctx, deps.Config.Repo, deps.Config.BranchName, targetBranch)
	if err != nil {
		return Result{}, err
	}

	// Step 5 + 6: file check + write.
	writeNeeded, _, err := writeFileIfNeeded(ctx, logger, deps, fileBranch, parentBranch, targetBranch)
	if err != nil {
		return Result{}, err
	}

	// Step 7: short-circuit when the bundle already matches and no work
	// remains to do. If the bundle matches but no MR exists yet and the
	// source branch was freshly created from the target, we know the
	// branch was created by THIS run and the file on it is whatever
	// the target branch has — so if it matches the source there's
	// truly nothing to do. If the branch already existed (parentBranch
	// is empty), the user might still want an MR opened, so we fall
	// through to CreateMR.
	if !writeNeeded {
		if existingMR != nil {
			logger.Info(fmt.Sprintf(msgExistingMR, existingMR.WebURL))
			return Result{Skipped: true, MRURL: existingMR.WebURL}, nil
		}
		if parentBranch == targetBranch && parentBranch != "" {
			logger.Info(msgNoUpdateNeeded)
			return Result{Skipped: true}, nil
		}
		// else: file matches and either no MR exists or the branch
		// already existed — fall through to CreateMR.
	}

	// Step 8: MR.
	if existingMR != nil {
		return Result{MRURL: existingMR.WebURL}, nil
	}
	logger.Info(msgCreatingMR)
	mr, err := deps.Client.CreateMR(ctx, deps.Config.Repo, platforms.CreateMRInput{
		SourceBranch: deps.Config.BranchName,
		TargetBranch: targetBranch,
		Title:        deps.Config.MRTitle,
		Description:  deps.Config.MRDescription,
	})
	if err != nil {
		// 422: a concurrent run opened the MR between our List and Create.
		// Re-list and reuse if possible.
		if e := platforms.As(err); e != nil && e.StatusCode == http.StatusUnprocessableEntity {
			retryMR, listErr := deps.Client.ListOpenMR(ctx, deps.Config.Repo, deps.Config.BranchName, targetBranch)
			if listErr == nil && retryMR != nil {
				logger.Info(fmt.Sprintf(msgExistingMR, retryMR.WebURL))
				return Result{MRURL: retryMR.WebURL}, nil
			}
		}
		return Result{}, err
	}
	logger.Info(fmt.Sprintf(msgMRCreated, mr.WebURL))
	return Result{MRURL: mr.WebURL}, nil
}

// writeFileIfNeeded compares the source bundle against the file at
// (Repo, TargetPath, fileBranch). It writes the file when it differs from
// the source (or doesn't exist), and reports back:
//
//   - writeNeeded:  true if the caller must continue to MR creation;
//     false if the file already matches (and no MR existed
//     with a non-default source branch — the caller must
//     still create the MR).
//   - lastCommitID: populated when a PUT is needed; empty for POST.
//
// The parentBranch arg is passed as CreateFile's startBranch so the
// platform can auto-create the new branch atomically when needed.
// It's empty when the branch already existed; passing it as the
// existing branch's own name causes GitLab's Files::CreateService
// to invoke Branches::CreateService a second time and fail with
// "A branch called 'X' already exists" (HTTP 400).
//
// Note: callers must still call CreateMR when writeNeeded is false but
// the source branch was freshly created from the target (file matches,
// no MR yet — caller short-circuits with Skipped=true).
func writeFileIfNeeded(ctx context.Context, logger *slog.Logger, deps Deps, fileBranch, parentBranch, targetBranch string) (writeNeeded bool, lastCommitID string, err error) {
	file, ferr := deps.Client.GetFile(ctx, deps.Config.Repo, deps.Config.TargetPath, fileBranch)
	if ferr != nil {
		if e := platforms.As(ferr); e == nil || e.Kind != platforms.KindNotFound {
			return false, "", ferr
		}
		// File does not exist — POST it. Pass parentBranch (the
		// parent branch) as startBranch so platforms that require
		// it (GitLab) can auto-create the new branch atomically.
		// When the branch already exists, parentBranch is "" and
		// no start_branch is sent to GitLab — see the comment at
		// the call site.
		logger.Info(fmt.Sprintf(msgCreatingFile, deps.Config.TargetPath, deps.Config.Repo))
		if cerr := deps.Client.CreateFile(ctx, deps.Config.Repo,
			deps.Config.BranchName, deps.Config.TargetPath, parentBranch,
			deps.Config.CommitMessage, bytes.NewReader(deps.Source)); cerr != nil {
			return false, "", cerr
		}
		logger.Info(fmt.Sprintf(msgFileUpdated, "POST", deps.Config.BranchName))
		return true, "", nil
	}

	if bytes.Equal(file.Content, deps.Source) {
		logger.Info(fmt.Sprintf(msgBundleMatches, deps.Config.TargetPath))
		// File matches; caller decides whether to short-circuit or to
		// still create an MR (which is the caller's responsibility).
		return false, "", nil
	}

	// File exists but differs — PUT. startBranch is unused for the
	// platforms that already had a way to update (GitHub needs the
	// branch to exist; the bundler ensured that in step 3 via
	// CreateBranch).
	logger.Info(fmt.Sprintf(msgUpdatingFile, deps.Config.TargetPath, deps.Config.Repo))
	if uerr := deps.Client.UpdateFile(ctx, deps.Config.Repo,
		deps.Config.BranchName, deps.Config.TargetPath, targetBranch,
		deps.Config.CommitMessage, file.LastCommitID, bytes.NewReader(deps.Source)); uerr != nil {
		return false, "", uerr
	}
	logger.Info(fmt.Sprintf(msgFileUpdated, "PUT", deps.Config.BranchName))
	return true, file.LastCommitID, nil
}
