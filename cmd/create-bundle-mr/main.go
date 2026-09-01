// Command create-bundle-mr creates or updates a GitLab merge request that
// delivers an updated CA certificate bundle to an external repository.
//
// See README.md for usage, flags, env vars, exit codes, and migration notes
// from the bash script this tool replaces.
//
// The CLI grammar lives in cli.go. This stub main() exists as a placeholder
// until step 6 wires it together with logging and the bundler workflow.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "create-bundle-mr: not yet implemented (step 6 will wire main)")
	os.Exit(7)
}
