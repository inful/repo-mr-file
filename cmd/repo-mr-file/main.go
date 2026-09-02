// Command repo-mr-file creates or updates a GitLab, GitHub, Gitea, or
// Forgejo merge/pull request that delivers an updated file to an external
// repository.
//
// See README.md for usage, flags, env vars, exit codes, and supported
// platforms.
//
// The CLI grammar lives in cli.go and the workflow in internal/bundler.
// This file wires them together: parses flags, builds a platform-specific
// client (or a dry-run / recording client), invokes the bundler, maps
// typed errors to exit codes, and prints the merge-request URL on success.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/alecthomas/kong"
	gogithub "github.com/google/go-github/v74/github"

	"github.com/inful/repo-mr-file/internal/bundler"
	"github.com/inful/repo-mr-file/internal/logging"
	"github.com/inful/repo-mr-file/internal/platforms"
	giteaplatform "github.com/inful/repo-mr-file/internal/platforms/gitea"
	githubplatform "github.com/inful/repo-mr-file/internal/platforms/github"
	gitlabplatform "github.com/inful/repo-mr-file/internal/platforms/gitlab"
)

// buildLiveClient constructs the platform-specific Client based on
// --platform and wraps it with the configured retry policy. The
// returned client satisfies platforms.Client.
//
//   - gitlab:   uses the GitLab official Go client
//   - github:   uses google/go-github; needs a GitHub username for PR
//     "head" formatting (--github-user, required for github)
//   - gitea:    uses net/http against the Gitea REST API
//   - forgejo:  same as gitea (API-compatible fork)
func buildLiveClient(cli *CLI, retryCfg platforms.RetryConfig) platforms.Client {
	// Short HTTP timeout so tests against unreachable URLs fail fast
	// and CI doesn't hang on transient network errors.
	httpClient := &http.Client{Timeout: 30 * time.Second}
	switch cli.Platform {
	case "github":
		ghc := gogithub.NewClient(httpClient)
		oc, err := githubplatform.NewClient(ghc, cli.GitHubUser, cli.APIToken, cli.APIBase)
		if err != nil {
			return platforms.NewAlwaysFailingClient(fmt.Errorf("github: %w", err))
		}
		return platforms.WithRetry(oc, retryCfg)
	case "gitea", "forgejo":
		oc := giteaplatform.NewOfficialClient(cli.APIBase, cli.APIToken)
		return platforms.WithRetry(oc, retryCfg)
	default: // "gitlab"
		oc := gitlabplatform.NewOfficialClient(cli.APIBase, cli.APIToken)
		return platforms.WithRetry(oc, retryCfg)
	}
}

// exitCodeFromError maps a typed *platforms.Error to a process exit code, as
// documented in the README exit-code table. Unknown kinds return 7
// (unexpected internal error).
func exitCodeFromError(err error) int {
	e := platforms.As(err)
	if e == nil {
		return 7
	}
	switch e.Kind {
	case platforms.KindConfig:
		return 2
	case platforms.KindAuth:
		return 3
	case platforms.KindNotFound:
		return 4
	case platforms.KindConflict:
		return 5
	case platforms.KindTransient:
		return 6
	default:
		return 7
	}
}

// run is the testable entry point. It parses args, builds deps, calls the
// bundler, and returns the process exit code. stdout / stderr are taken as
// io.Writer so tests can capture output; clientOverride lets tests inject a
// fake Client (pass nil to use --dry-run or build from CLI).
func run(args []string, stdout, stderr io.Writer, clientOverride platforms.Client) int {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Exit(func(int) {}),
		kong.Name("repo-mr-file"),
		kong.Description("Create or update a GitLab, GitHub, Gitea, or Forgejo merge/pull request."),
	)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 7
	}
	if _, err := parser.Parse(args); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	logger := logging.New(stderr, cli.LogFormat, cli.Verbose)

	// Read the source file.
	source, err := os.ReadFile(cli.SourcePath)
	if err != nil {
		logger.Error("read source file", "path", cli.SourcePath, "err", err.Error())
		return 2
	}

	// Build the platform-specific client (or use the override).
	retryCfg := platforms.RetryConfig{
		MaxAttempts:    cli.Retries + 1, // --retries counts additional attempts
		InitialBackoff: cli.RetryBackoff,
		Logger:         logger,
	}
	var client platforms.Client
	switch {
	case clientOverride != nil:
		// Apply the configured retry policy to injected clients too so
		// tests can verify retry semantics end-to-end.
		client = platforms.WithRetry(clientOverride, retryCfg)
	case cli.DryRun:
		client = platforms.NewDryRunClient()
	default:
		client = buildLiveClient(&cli, retryCfg)
	}

	deps := bundler.Deps{
		Client: client,
		Logger: logger,
		Config: bundler.Config{
			Label:         cli.Label,
			Repo:          cli.Repo,
			TargetPath:    cli.TargetPath,
			TargetBranch:  cli.TargetBranch,
			BranchName:    cli.BranchName,
			CommitMessage: cli.CommitMessage,
			MRTitle:       cli.MRTitle,
			MRDescription: cli.MRDescription,
		},
		Source: source,
		DryRun: cli.DryRun,
	}

	result, err := bundler.Run(context.Background(), deps)
	if err != nil {
		logger.Error("bundler failed", "err", err.Error())
		return exitCodeFromError(err)
	}

	if result.MRURL != "" {
		_, _ = fmt.Fprintln(stdout, result.MRURL)
	}
	return 0
}

// main parses os.Args and calls os.Exit with the code returned by run.
func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr, nil)
	os.Exit(code)
}
