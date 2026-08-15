package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var expectedRootCommands = []string{
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

func TestRunDispatchesSupportedDesignCommands(t *testing.T) {
	if !reflect.DeepEqual(rootCommandNames, expectedRootCommands) {
		t.Fatalf("rootCommandNames = %#v, want %#v", rootCommandNames, expectedRootCommands)
	}

	for _, name := range expectedRootCommands {
		t.Run(name, func(t *testing.T) {
			if _, ok := rootCommands[name]; !ok {
				t.Fatalf("root command %q is not registered", name)
			}
		})
	}
}

func TestRunRejectsUnknownCommandWithDocktreeUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"wat"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("unknown exit = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "unknown command \"wat\"\n" + expectedUsageText()
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunHelpPrintsDocktreeUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("help exit = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if got, want := stdout.String(), expectedUsageText(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestREADMECommandTableStaysAlignedWithCLI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	commandLine := regexp.MustCompile("^\\| `docktree ([a-z]+)(?:[ `]|$)")
	var got []string
	for _, line := range strings.Split(string(data), "\n") {
		if match := commandLine.FindStringSubmatch(line); match != nil {
			got = append(got, match[1])
		}
	}
	if !reflect.DeepEqual(got, expectedRootCommands) {
		t.Fatalf("README commands = %#v, want %#v", got, expectedRootCommands)
	}
}

func expectedUsageText() string {
	return "usage: docktree <command> [args]\ncommands: " + strings.Join(expectedRootCommands, ", ") + "\n"
}
