// Command create-bundle-mr creates or updates a GitLab merge request that
// delivers an updated CA certificate bundle to an external repository.
//
// See README.md for usage, flags, env vars, exit codes, and migration notes
// from the bash script this tool replaces.
//
// This file is a stub that exists so the module satisfies golangci-lint's
// requirement of at least one Go source file. Step 2 of the bootstrap
// replaces it with the kong-driven CLI implementation.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "create-bundle-mr: not yet implemented (bootstrap pending)")
	os.Exit(7)
}
