// Package bundler orchestrates the 8-step workflow that creates or updates
// a GitLab merge request which delivers an updated CA certificate bundle
// to an external repository.
//
// The public Run entry point accepts a Deps value (Client, Logger, Config,
// Bundle, DryRun) and returns a Result plus a typed error. All API calls
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

	"github.com/inful/gitlab-mr-file/internal/gitlab"
	"github.com/inful/gitlab-mr-file/internal/logging"
)

// Config is the subset of CLI fields the bundler needs.
type Config struct {
	Tag           string
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
	Client gitlab.Client
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
func Run(ctx context.Context, deps Deps) (Result, error) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	if deps.DryRun {
		return runDry(ctx, logger, deps)
	}

	// Step 1: project.
	logger.Info(fmt.Sprintf(logging.MsgGettingProjectInfo, deps.Config.Repo))
	proj, err := deps.Client.GetProject(ctx, deps.Config.Repo)
	if err != nil {
		return Result{}, err
	}
	logger.Info(fmt.Sprintf(logging.MsgFoundProjectID, proj.ID))

	// Step 2: target branch.
	targetBranch := deps.Config.TargetBranch
	if targetBranch == "" {
		targetBranch = proj.DefaultBranch
	}
	if targetBranch == "" {
		return Result{}, gitlab.New(gitlab.KindConfig, "Run",
			errors.New("project has no default branch and --target-branch was not set"))
	}
	logger.Info(fmt.Sprintf(logging.MsgUsingTargetBranch, targetBranch))

	// Step 3: source branch.
	logger.Info(fmt.Sprintf(logging.MsgCheckingBranch, deps.Config.BranchName))
	branchExists, err := deps.Client.GetBranch(ctx, deps.Config.Repo, deps.Config.BranchName)
	if err != nil {
		return Result{}, err
	}
	sourceBranch := targetBranch
	if branchExists {
		logger.Info(logging.MsgBranchExists)
		sourceBranch = deps.Config.BranchName
	} else {
		logger.Info(fmt.Sprintf(logging.MsgBranchDoesNotExist, targetBranch))
	}

	// Step 4: existing MR.
	existingMR, err := deps.Client.ListOpenMR(ctx, deps.Config.Repo, deps.Config.BranchName, targetBranch)
	if err != nil {
		return Result{}, err
	}

	// Step 5 + 6: file check + write.
	writeNeeded, _, err := writeFileIfNeeded(ctx, logger, deps, sourceBranch)
	if err != nil {
		return Result{}, err
	}

	// Step 7: short-circuit when the bundle already matches and no work
	// remains to do. If the bundle matches but no MR exists yet and the
	// source branch differs from the target, we still need to create
	// the MR (without writing the file).
	if !writeNeeded {
		if existingMR != nil {
			logger.Info(fmt.Sprintf(logging.MsgExistingMR, existingMR.WebURL))
			return Result{Skipped: true, MRURL: existingMR.WebURL}, nil
		}
		if sourceBranch == targetBranch {
			logger.Info(logging.MsgNoUpdateNeeded)
			return Result{Skipped: true}, nil
		}
		// else: file matches but source != target and no MR — fall
		// through to CreateMR.
	}

	// Step 8: MR.
	if existingMR != nil {
		return Result{MRURL: existingMR.WebURL}, nil
	}
	logger.Info(logging.MsgCreatingMR)
	mr, err := deps.Client.CreateMR(ctx, deps.Config.Repo, gitlab.CreateMRInput{
		SourceBranch: deps.Config.BranchName,
		TargetBranch: targetBranch,
		Title:        deps.Config.MRTitle,
		Description:  deps.Config.MRDescription,
	})
	if err != nil {
		// 422: a concurrent run opened the MR between our List and Create.
		// Re-list and reuse if possible.
		if e := gitlab.As(err); e != nil && e.StatusCode == http.StatusUnprocessableEntity {
			retryMR, listErr := deps.Client.ListOpenMR(ctx, deps.Config.Repo, deps.Config.BranchName, targetBranch)
			if listErr == nil && retryMR != nil {
				logger.Info(fmt.Sprintf(logging.MsgExistingMR, retryMR.WebURL))
				return Result{MRURL: retryMR.WebURL}, nil
			}
		}
		return Result{}, err
	}
	logger.Info(fmt.Sprintf(logging.MsgMRCreated, mr.WebURL))
	return Result{MRURL: mr.WebURL}, nil
}

// writeFileIfNeeded compares the source bundle against the file at
// (Repo, TargetPath, sourceBranch). It writes the file when it differs from
// the source (or doesn't exist), and reports back:
//
//   - writeNeeded:  true if the caller must continue to MR creation;
//     false if the file already matches (and no MR existed
//     with a non-default source branch — the caller must
//     still create the MR).
//   - lastCommitID: populated when a PUT is needed; empty for POST.
//
// Note: callers must still call CreateMR when writeNeeded is false but
// the source branch differs from the target (file matches, no MR yet —
// we still want the MR).
func writeFileIfNeeded(ctx context.Context, logger *slog.Logger, deps Deps, sourceBranch string) (writeNeeded bool, lastCommitID string, err error) {
	file, ferr := deps.Client.GetFile(ctx, deps.Config.Repo, deps.Config.TargetPath, sourceBranch)
	if ferr != nil {
		if e := gitlab.As(ferr); e == nil || e.Kind != gitlab.KindNotFound {
			return false, "", ferr
		}
		// File does not exist — POST it.
		logger.Info(fmt.Sprintf(logging.MsgCreatingFile, deps.Config.TargetPath, deps.Config.Repo))
		if cerr := deps.Client.CreateFile(ctx, deps.Config.Repo,
			deps.Config.BranchName, deps.Config.TargetPath,
			deps.Config.CommitMessage, bytes.NewReader(deps.Source)); cerr != nil {
			return false, "", cerr
		}
		logger.Info(fmt.Sprintf(logging.MsgFileUpdated, "POST", deps.Config.BranchName))
		return true, "", nil
	}

	if bytes.Equal(file.Content, deps.Source) {
		logger.Info(fmt.Sprintf(logging.MsgBundleMatches, deps.Config.TargetPath))
		// File matches; caller decides whether to short-circuit or to
		// still create an MR (which is the caller's responsibility).
		return false, "", nil
	}

	// File exists but differs — PUT.
	logger.Info(fmt.Sprintf(logging.MsgUpdatingFile, deps.Config.TargetPath, deps.Config.Repo))
	if uerr := deps.Client.UpdateFile(ctx, deps.Config.Repo,
		deps.Config.BranchName, deps.Config.TargetPath,
		deps.Config.CommitMessage, file.LastCommitID, bytes.NewReader(deps.Source)); uerr != nil {
		return false, "", uerr
	}
	logger.Info(fmt.Sprintf(logging.MsgFileUpdated, "PUT", deps.Config.BranchName))
	return true, file.LastCommitID, nil
}

// runDry simulates the workflow when --dry-run is set: it uses a client
// that records every call without making real requests, and logs each
// intended step.
func runDry(ctx context.Context, logger *slog.Logger, deps Deps) (Result, error) {
	logger.Info(fmt.Sprintf(logging.MsgGettingProjectInfo, deps.Config.Repo))
	logger.Info(fmt.Sprintf(logging.MsgCheckingBranch, deps.Config.BranchName))
	logger.Info(fmt.Sprintf(logging.MsgBranchDoesNotExist, deps.Config.TargetBranch))

	// Drive the dry-run client through the same code path so the recorded
	// call sequence matches what live mode would do.
	if _, err := deps.Client.GetProject(ctx, deps.Config.Repo); err != nil {
		return Result{}, err
	}
	if _, err := deps.Client.GetBranch(ctx, deps.Config.Repo, deps.Config.BranchName); err != nil {
		return Result{}, err
	}
	if _, err := deps.Client.GetFile(ctx, deps.Config.Repo, deps.Config.TargetPath, deps.Config.TargetBranch); err != nil {
		// Dry-run client returns a synthetic KindNotFound; that's fine.
		if e := gitlab.As(err); e == nil || e.Kind != gitlab.KindNotFound {
			return Result{}, err
		}
	}
	logger.Info(fmt.Sprintf(logging.MsgCreatingFile, deps.Config.TargetPath, deps.Config.Repo))
	if err := deps.Client.CreateFile(ctx, deps.Config.Repo,
		deps.Config.BranchName, deps.Config.TargetPath,
		deps.Config.CommitMessage, bytes.NewReader(deps.Source)); err != nil {
		return Result{}, err
	}
	logger.Info(fmt.Sprintf(logging.MsgFileUpdated, "POST", deps.Config.BranchName))
	logger.Info(logging.MsgCreatingMR)
	mr, err := deps.Client.CreateMR(ctx, deps.Config.Repo, gitlab.CreateMRInput{
		SourceBranch: deps.Config.BranchName,
		TargetBranch: deps.Config.TargetBranch,
		Title:        deps.Config.MRTitle,
		Description:  deps.Config.MRDescription,
	})
	if err != nil {
		return Result{}, err
	}
	logger.Info(fmt.Sprintf(logging.MsgMRCreated, mr.WebURL))
	return Result{DryRun: true, MRURL: mr.WebURL}, nil
}
