// Command create-bundle-mr creates or updates a GitLab merge request that
// delivers an updated CA certificate bundle to an external repository.
//
// See README.md for usage, flags, env vars, exit codes, and migration notes
// from the bash script this tool replaces.
//
// The CLI grammar lives in cli.go and the workflow in internal/bundler.
// This file wires them together: parses flags, builds the GitLab client
// (or a dry-run / recording client), invokes the bundler, maps typed
// errors to exit codes, and prints the merge-request URL on success.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/alecthomas/kong"

	"github.com/inful/updateext/internal/bundler"
	"github.com/inful/updateext/internal/gitlab"
	"github.com/inful/updateext/internal/logging"
)

// exitCodeFromError maps a typed *gitlab.Error to a process exit code, as
// documented in the README exit-code table. Unknown kinds return 7
// (unexpected internal error).
func exitCodeFromError(err error) int {
	e := gitlab.As(err)
	if e == nil {
		return 7
	}
	switch e.Kind {
	case gitlab.KindConfig:
		return 2
	case gitlab.KindAuth:
		return 3
	case gitlab.KindNotFound:
		return 4
	case gitlab.KindConflict:
		return 5
	case gitlab.KindTransient:
		return 6
	default:
		return 7
	}
}

// run is the testable entry point. It parses args, builds deps, calls the
// bundler, and returns the process exit code. stdout / stderr are taken as
// io.Writer so tests can capture output; clientOverride lets tests inject a
// fake Client (pass nil to use --dry-run or build from CLI).
func run(args []string, stdout, stderr io.Writer, clientOverride gitlab.Client) int {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Exit(func(int) {}),
		kong.Name("create-bundle-mr"),
		kong.Description("Create or update a GitLab merge request that delivers an updated CA certificate bundle."),
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

	// Read the source bundle.
	bundle, err := os.ReadFile(cli.Bundle)
	if err != nil {
		logger.Error("read bundle", "path", cli.Bundle, "err", err.Error())
		return 2
	}

	// Build the GitLab client (or use the override).
	retryCfg := gitlab.RetryConfig{
		MaxAttempts:    cli.Retries + 1, // --retries counts additional attempts
		InitialBackoff: cli.RetryBackoff,
		Logger:         logger,
	}
	var client gitlab.Client
	switch {
	case clientOverride != nil:
		// Apply the configured retry policy to injected clients too so
		// tests can verify retry semantics end-to-end.
		client = gitlab.WithRetry(clientOverride, retryCfg)
	case cli.DryRun:
		client = gitlab.NewDryRunClient()
	default:
		oc := gitlab.NewOfficialClient(cli.GitLabAPI, cli.GitLabToken)
		client = gitlab.WithRetry(oc, retryCfg)
	}

	deps := bundler.Deps{
		Client: client,
		Logger: logger,
		Config: bundler.Config{
			Tag:           cli.Tag,
			Repo:          cli.Repo,
			CertPath:      cli.CertPath,
			TargetBranch:  cli.TargetBranch,
			BranchName:    cli.BranchName,
			CommitMessage: cli.CommitMessage,
			MRTitle:       cli.MRTitle,
			MRDescription: cli.MRDescription,
		},
		Bundle: bundle,
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
