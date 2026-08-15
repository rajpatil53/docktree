package main

import (
	"os"

	"github.com/rajpatil53/docktree/cmd"
)

func main() {
	os.Exit(cmd.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
