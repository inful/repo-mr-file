package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
)

// writeBundle creates a temp CA-bundle file and returns its path. Tests use
// this to satisfy the required --source-path flag.
func writeBundle(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca-bundle.pem")
	if err := os.WriteFile(p, []byte("dummy ca bundle content"), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return p
}

// validArgs returns the minimum set of CLI args that makes the CLI happy
// (all required flags provided, bundle points at a real file).
func validArgs(bundle string) []string {
	return []string{
		"--label=v1.2.3",
		"--repo=some/project",
		"--target-path=ca.pem",
		"--source-path=" + bundle,
		"--api-token=secret",
	}
}

// parseCLI constructs a kong parser (with a no-op Exit override so tests
// don't accidentally exit the process) and parses the given args. The
// returned CLI is populated only when err is nil.
func parseCLI(t *testing.T, args ...string) (CLI, error) {
	t.Helper()
	var cli CLI
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		return cli, err
	}
	_, err = parser.Parse(args)
	return cli, err
}

// mustParseCLI parses args and fails the test on any error.
func mustParseCLI(t *testing.T, args ...string) CLI {
	t.Helper()
	cli, err := parseCLI(t, args...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cli
}

// --- required flags ---

func TestCLI_RequiredFlagsMissing(t *testing.T) {
	bundle := writeBundle(t)
	cases := []struct {
		name    string
		args    []string
		missing string
	}{
		{"label", []string{"--repo=r", "--target-path=c", "--source-path=" + bundle, "--api-token=t"}, "--label"},
		{"repo", []string{"--label=t", "--target-path=c", "--source-path=" + bundle, "--api-token=t"}, "--repo"},
		{"target-path", []string{"--label=t", "--repo=r", "--source-path=" + bundle, "--api-token=t"}, "--target-path"},
		{"bundle", []string{"--label=t", "--repo=r", "--target-path=c", "--api-token=t"}, "--source-path"},
		{"api-token", []string{"--label=t", "--repo=r", "--target-path=c", "--source-path=" + bundle}, "--api-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("expected error for missing %s, got nil", tc.missing)
			}
			if !strings.Contains(err.Error(), tc.missing) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.missing)
			}
		})
	}
}

// --- env var binding ---

func TestCLI_EnvBindingAPIBase(t *testing.T) {
	bundle := writeBundle(t)
	t.Setenv("API_BASE", "https://gitlab.example.com/api/v4")
	t.Setenv("API_TOKEN", "env-token")

	cli := mustParseCLI(t,
		"--label=t", "--repo=r", "--target-path=c", "--source-path="+bundle,
	)

	if got, want := cli.APIBase, "https://gitlab.example.com/api/v4"; got != want {
		t.Errorf("APIBase = %q, want %q (from env)", got, want)
	}
	if got, want := cli.APIToken, "env-token"; got != want {
		t.Errorf("APIToken = %q, want %q (from env)", got, want)
	}
}

func TestCLI_FlagOverridesEnv(t *testing.T) {
	bundle := writeBundle(t)
	t.Setenv("API_BASE", "https://env.example.com/api/v4")

	cli := mustParseCLI(t,
		"--label=t", "--repo=r", "--target-path=c", "--source-path="+bundle, "--api-token=tk",
		"--api-base=https://flag.example.com/api/v4",
	)

	if got, want := cli.APIBase, "https://flag.example.com/api/v4"; got != want {
		t.Errorf("APIBase = %q, want %q (flag overrides env)", got, want)
	}
}

// --- defaults ---

func TestCLI_Defaults(t *testing.T) {
	bundle := writeBundle(t)
	cli := mustParseCLI(t, validArgs(bundle)...)

	if got, want := cli.APIBase, "https://gitlab.mgmlab.net/api/v4"; got != want {
		t.Errorf("APIBase = %q, want default %q", got, want)
	}
	if got, want := cli.Retries, 3; got != want {
		t.Errorf("Retries = %d, want %d", got, want)
	}
	if got, want := cli.RetryBackoff, 500*time.Millisecond; got != want {
		t.Errorf("RetryBackoff = %v, want %v", got, want)
	}
	if got, want := cli.LogFormat, "text"; got != want {
		t.Errorf("LogFormat = %q, want %q", got, want)
	}
	if cli.Verbose {
		t.Error("Verbose = true, want false")
	}
	if cli.DryRun {
		t.Error("DryRun = true, want false")
	}
}

// --- bool flags ---

func TestCLI_BoolFlags(t *testing.T) {
	bundle := writeBundle(t)
	cli := mustParseCLI(t,
		"--label=t", "--repo=r", "--target-path=c", "--source-path="+bundle, "--api-token=tk",
		"--verbose", "--dry-run",
	)

	if !cli.Verbose {
		t.Error("Verbose = false, want true")
	}
	if !cli.DryRun {
		t.Error("DryRun = false, want true")
	}
}

// --- AfterApply: templated defaults ---

func TestCLI_AfterApplyTemplating(t *testing.T) {
	bundle := writeBundle(t)
	cli := mustParseCLI(t,
		"--label=v9.9.9", "--repo=r", "--target-path=docs/ips.txt", "--source-path="+bundle, "--api-token=tk",
	)

	if got, want := cli.BranchName, "update-v9.9.9"; got != want {
		t.Errorf("BranchName = %q, want %q", got, want)
	}
	if got, want := cli.CommitMessage, "Update docs/ips.txt to v9.9.9"; got != want {
		t.Errorf("CommitMessage = %q, want %q", got, want)
	}
	if cli.MRTitle != cli.CommitMessage {
		t.Errorf("MRTitle = %q, want %q (defaulted from CommitMessage)", cli.MRTitle, cli.CommitMessage)
	}
	if got, want := cli.MRDescription, "Updates docs/ips.txt to v9.9.9."; got != want {
		t.Errorf("MRDescription = %q, want %q", got, want)
	}
}

func TestCLI_AfterApplyAPIURLDerived(t *testing.T) {
	bundle := writeBundle(t)
	cli := mustParseCLI(t, validArgs(bundle)...)

	if got, want := cli.APIURL, "https://gitlab.mgmlab.net"; got != want {
		t.Errorf("APIURL = %q, want %q (derived from default APIBase)", got, want)
	}
}

func TestCLI_AfterApplyDoesNotOverwriteExplicit(t *testing.T) {
	bundle := writeBundle(t)
	cli := mustParseCLI(t,
		"--label=t", "--repo=r", "--target-path=c", "--source-path="+bundle, "--api-token=tk",
		"--branch-name=custom-branch",
		"--commit-message=custom message",
		"--mr-title=custom title",
		"--mr-description=custom desc",
		"--api-url=https://my.gitlab.example.com",
	)

	if got, want := cli.BranchName, "custom-branch"; got != want {
		t.Errorf("BranchName = %q, want %q", got, want)
	}
	if got, want := cli.CommitMessage, "custom message"; got != want {
		t.Errorf("CommitMessage = %q, want %q", got, want)
	}
	if got, want := cli.MRTitle, "custom title"; got != want {
		t.Errorf("MRTitle = %q, want %q", got, want)
	}
	if got, want := cli.APIURL, "https://my.gitlab.example.com"; got != want {
		t.Errorf("APIURL = %q, want %q", got, want)
	}
}

// --- Validate ---

func TestCLI_ValidateRejectsMissingBundle(t *testing.T) {
	_, err := parseCLI(t,
		"--label=t", "--repo=r", "--target-path=c",
		"--source-path=/nonexistent/path/ca.pem",
		"--api-token=tk",
	)
	if err == nil {
		t.Fatal("expected error for missing bundle file")
	}
	if !strings.Contains(err.Error(), "bundle") && !strings.Contains(err.Error(), "/nonexistent") {
		t.Errorf("error %q does not mention bundle/path", err.Error())
	}
}

func TestCLI_ValidateRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := parseCLI(t,
		"--label=t", "--repo=r", "--target-path=c",
		"--source-path="+dir,
		"--api-token=tk",
	)
	if err == nil {
		t.Fatal("expected error for bundle being a directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error %q does not mention directory", err.Error())
	}
}

// --- enum validation ---

func TestCLI_LogFormatEnum(t *testing.T) {
	bundle := writeBundle(t)
	_, err := parseCLI(t,
		"--label=t", "--repo=r", "--target-path=c", "--source-path="+bundle, "--api-token=tk",
		"--log-format=xml",
	)
	if err == nil {
		t.Fatal("expected error for invalid --log-format")
	}
	if !strings.Contains(err.Error(), "log-format") {
		t.Errorf("error %q does not mention log-format", err.Error())
	}
}
