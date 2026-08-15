package cmd

import (
	"fmt"
	"io"
)

// These values are overridden in release builds with Go linker flags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func runVersion(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: docktree version")
	}
	printVersion(stdout)
	return nil
}

func printVersion(stdout io.Writer) {
	fmt.Fprintf(stdout, "docktree version %s (commit %s, built %s)\n", Version, Commit, Date)
}
