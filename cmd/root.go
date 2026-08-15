package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type rootHandler func(args []string, stdin io.Reader, stdout, stderr io.Writer) error

var rootCommandNames = []string{
	"up",
	"down",
	"ls",
	"ps",
	"open",
	"exec",
	"logs",
	"shared",
	"fork",
	"unfork",
	"explain",
	"prune",
	"init",
	"doctor",
	"proxy",
	"trust",
}

var rootCommands = func() map[string]rootHandler {
	commands := map[string]rootHandler{
		"up": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runUp(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"down": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runDown(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"shared": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runShared(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"ls": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runLS(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"open": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runOpen(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"ps": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runPS(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"logs": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runLogs(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"exec": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runExec(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"explain": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runExplain(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"doctor": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runDoctor(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"init": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runInit(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"fork": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runFork(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"unfork": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runUnfork(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"prune": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runPrune(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"proxy": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runProxy(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
		"trust": func(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return runTrust(context.Background(), args, defaultCommandDeps(stdin, stdout, stderr))
		},
	}
	return commands
}()

// Run dispatches the Docktree CLI without calling os.Exit, so tests can assert
// routing and output behavior directly.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	name := args[0]
	switch name {
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	}

	handler, ok := rootCommands[name]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n", name)
		printUsage(stderr)
		return 2
	}
	if err := handler(args[1:], stdin, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: docktree <command> [args]")
	fmt.Fprintln(w, "commands: "+strings.Join(rootCommandNames, ", "))
}
