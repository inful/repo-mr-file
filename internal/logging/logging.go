// Package logging configures the slog logger used by repo-mr-file.
//
// The bash script this binary replaces emits specific echo lines that
// operators may grep for in CI logs. The Msg* constants here capture those
// lines verbatim so callers can reuse them via fmt.Sprintf, while New()
// returns a *slog.Logger configured with the chosen format ("text" or
// "json") and verbosity.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// Bash-mirroring echo lines used by the create-bundle-mr workflow. Callers
// format these with fmt.Sprintf using the documented verbs.
const (
	MsgGettingProjectInfo = "Getting project info for %s..."
	MsgFoundProjectID     = "Found project ID: %d"
	MsgUsingTargetBranch  = "Using target branch: %s"
	MsgCheckingBranch     = "Checking if branch %s exists..."
	MsgBranchDoesNotExist = "Branch does not exist, will create from %s..."
	MsgBranchExists       = "Branch exists, will update existing branch"
	MsgBundleMatches      = "%s already matches the source bundle"
	MsgUpdatingFile       = "Updating %s in %s..."
	MsgCreatingFile       = "Creating %s in %s..."
	MsgFileUpdated        = "✓ File %s completed in branch %s"
	MsgCreatingMR         = "Creating merge request..."
	MsgMRCreated          = "✓ Merge request created: %s"
	MsgExistingMR         = "✓ Existing MR: %s"
	MsgNoUpdateNeeded     = "✓ No update or merge request is needed"
)

// New returns a *slog.Logger writing to w. format is "text" or "json";
// any other value falls back to text. When verbose is true the handler
// emits at debug level; otherwise at info level (debug is filtered).
func New(w io.Writer, format string, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = slog.NewTextHandler(w, opts)
	}
	return slog.New(handler)
}
