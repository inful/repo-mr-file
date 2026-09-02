package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// CLI holds all flags and env-var bindings for the create-bundle-mr binary.
//
// Defaults and required fields are expressed via kong struct tags.
// Templated defaults (e.g. branch name based on --tag) are populated by the
// AfterApply hook; file-existence checks are done in Validate.
type CLI struct {
	// Required inputs.
	Tag        string `required:"" help:"Release tag that identifies this update."`
	Repo       string `required:"" help:"GitLab project path to update."`
	TargetPath string `required:"" name:"target-path" help:"Path of the file inside the target repo."`
	SourcePath string `required:"" name:"source-path" help:"Local path to the source file."`

	// Platform connection (works for gitlab, github, gitea, forgejo).
	APIBase  string `default:"https://gitlab.mgmlab.net/api/v4" env:"API_BASE" name:"api-base" help:"Platform API base URL (e.g. https://host/api/v4 for GitLab, https://api.github.com for GitHub, https://host/api/v1 for Gitea/Forgejo)."`
	APIURL   string `env:"API_URL" name:"api-url" help:"Platform web URL (defaults to --api-base minus the API suffix)."`
	APIToken string `required:"" env:"API_TOKEN" name:"api-token" help:"Platform API token with the required scopes (api for GitLab, repo for GitHub, repo for Gitea/Forgejo)."`

	// Branching.
	TargetBranch string `env:"TARGET_BRANCH" name:"target-branch" help:"Branch to receive the MR. Defaults to the project default branch."`

	// Templates (substituted by AfterApply when left empty).
	BranchName    string `default:"" name:"branch-name" help:"Branch name (default: chore/update-ca-bundle-<tag>)."`
	CommitMessage string `default:"" name:"commit-message" help:"Commit message (default uses --tag value)."`
	MRTitle       string `default:"" name:"mr-title" help:"MR title (defaults to --commit-message)."`
	MRDescription string `default:"" name:"mr-description" help:"MR description (default template uses --tag value)."`

	// Retry policy.
	Retries      int           `default:"3" help:"Additional attempts per API call after 5xx/429."`
	RetryBackoff time.Duration `default:"500ms" name:"retry-backoff" help:"Initial backoff; exponential with jitter."`

	// Logging / execution mode.
	LogFormat string `default:"text" enum:"text,json" name:"log-format" help:"Log output format."`
	Verbose   bool   `help:"Enable debug logging."`
	DryRun    bool   `name:"dry-run" help:"Log intended API calls without making any."`

	// Platform selection.
	Platform string `default:"gitlab" enum:"gitlab,github,gitea,forgejo" name:"platform" help:"Target platform (gitlab, github, gitea, forgejo). forgejo reuses the gitea client."`
	// GitHubUser is required when --platform=github; the bundler needs
	// the GitHub handle that owns the token to format PR "head" fields
	// as "user:branch" (the format GitHub requires). Ignored for other
	// platforms.
	GitHubUser string `name:"github-user" help:"GitHub handle that owns the token (required when --platform=github)."`

	// Logger is populated by the caller after parsing; not a flag.
	Logger *slog.Logger `kong:"-"`
}

// AfterApply populates the templated defaults that reference --tag and
// derives --api-url from --api-base. Kong invokes this after
// command-line values have been applied and validation has succeeded.
//
// Templates are deliberately generic: the tool isn't tied to any one
// workflow. Override any of these via flags for project-specific wording.
func (c *CLI) AfterApply() error {
	if c.BranchName == "" {
		c.BranchName = fmt.Sprintf("update-%s", c.Tag)
	}
	if c.CommitMessage == "" {
		c.CommitMessage = fmt.Sprintf("Update %s to release %s", c.TargetPath, c.Tag)
	}
	if c.MRTitle == "" {
		c.MRTitle = c.CommitMessage
	}
	if c.MRDescription == "" {
		c.MRDescription = fmt.Sprintf(
			"Updates %s from release %s.",
			c.TargetPath, c.Tag,
		)
	}
	if c.APIURL == "" {
		// Best-effort derivation: strip a trailing /api/vN, /api/v4,
		// or /api/v3 from the base. Different platforms use different
		// suffixes; we try a few common ones.
		c.APIURL = strings.TrimSuffix(c.APIBase, "/api/v4")
		c.APIURL = strings.TrimSuffix(c.APIURL, "/api/v3")
		c.APIURL = strings.TrimSuffix(c.APIURL, "/api/v1")
	}
	return nil
}

// Validate confirms that the source file exists, is a regular file, and is
// readable. Kong invokes this before its built-in missing-flags check, so we
// skip the file check when SourcePath is empty and let kong surface the
// "missing required flag --source-path" error itself.
func (c *CLI) Validate() error {
	if c.SourcePath == "" {
		return nil
	}
	info, err := os.Stat(c.SourcePath)
	if err != nil {
		return fmt.Errorf("--source-path: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("--source-path: %s is a directory", c.SourcePath)
	}
	f, err := os.Open(c.SourcePath)
	if err != nil {
		return fmt.Errorf("--source-path: %w", err)
	}
	_ = f.Close()
	return nil
}
