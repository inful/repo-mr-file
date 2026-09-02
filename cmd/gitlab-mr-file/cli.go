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
	Tag      string `required:"" help:"Custom-certs release tag."`
	Repo     string `required:"" help:"GitLab project path to update."`
	CertPath string `required:"" name:"cert-path" help:"Path of the cert file inside the target repo."`
	Bundle   string `required:"" help:"Local path to the source CA bundle."`

	// GitLab connection.
	GitLabAPI   string `default:"https://gitlab.mgmlab.net/api/v4" env:"GITLAB_API" name:"gitlab-api" help:"GitLab API base URL."`
	GitLabURL   string `env:"GITLAB_URL" name:"gitlab-url" help:"GitLab web URL (defaults to --gitlab-api minus /api/v4)."`
	GitLabToken string `required:"" env:"GITLAB_TOKEN" name:"gitlab-token" help:"GitLab API token with api scope."`

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

	// Logger is populated by the caller after parsing; not a flag.
	Logger *slog.Logger `kong:"-"`
}

// AfterApply populates the templated defaults that reference --tag and derives
// --gitlab-url from --gitlab-api. Kong invokes this after command-line values
// have been applied and validation has succeeded.
func (c *CLI) AfterApply() error {
	if c.BranchName == "" {
		c.BranchName = fmt.Sprintf("chore/update-ca-bundle-%s", c.Tag)
	}
	if c.CommitMessage == "" {
		c.CommitMessage = fmt.Sprintf("chore: update CA certificate bundle from custom-certs %s", c.Tag)
	}
	if c.MRTitle == "" {
		c.MRTitle = c.CommitMessage
	}
	if c.MRDescription == "" {
		c.MRDescription = fmt.Sprintf(
			"Updates the certificate bundle with the latest from the custom-certs release.\n\nGenerated from: custom-certs %s\nCA bundle source: https://gitlab.mgmlab.net/seksjon-for-bioinformatikk/custom-certs/-/releases/%s",
			c.Tag, c.Tag,
		)
	}
	if c.GitLabURL == "" {
		c.GitLabURL = strings.TrimSuffix(c.GitLabAPI, "/api/v4")
	}
	return nil
}

// Validate confirms that the source bundle file exists, is a regular file,
// and is readable. Kong invokes this before its built-in missing-flags check,
// so we skip the file check when Bundle is empty and let kong surface the
// "missing required flag --bundle" error itself.
func (c *CLI) Validate() error {
	if c.Bundle == "" {
		return nil
	}
	info, err := os.Stat(c.Bundle)
	if err != nil {
		return fmt.Errorf("--bundle: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("--bundle: %s is a directory", c.Bundle)
	}
	f, err := os.Open(c.Bundle)
	if err != nil {
		return fmt.Errorf("--bundle: %w", err)
	}
	_ = f.Close()
	return nil
}
