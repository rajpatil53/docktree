package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/runner"
)

// caRootInContainer is where Caddy stores its generated local-CA root inside the
// proxy container's persistent /data volume.
const caRootInContainer = "/data/caddy/pki/authorities/local/root.crt"

func Trust(args []string) error {
	return runTrust(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

// runTrust exports the proxy's Caddy local-CA root to a stable path and prints
// the one-time, OS-specific command to install it into the host trust store.
// docktree never invokes sudo itself; it hands the user the exact command.
func runTrust(ctx context.Context, args []string, deps commandDeps) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dest := filepath.Join(paths.ProxyDir(home), "root.crt")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	cp := runner.Command{
		Argv:   []string{"docker", "compose", "-p", proxyProject, "cp", "docktree-proxy:" + caRootInContainer, dest},
		Env:    os.Environ(),
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
	if err := deps.runner.Run(ctx, cp); err != nil {
		return fmt.Errorf("export Caddy root CA (is the proxy running? try `docktree proxy up`): %w", err)
	}
	fmt.Fprintf(deps.stdout, "Caddy root CA exported to %s\n", dest)
	fmt.Fprintln(deps.stdout, "Install it into your system trust store (one-time, requires sudo):")
	fmt.Fprintln(deps.stdout, trustInstallHint(dest))
	return nil
}

func trustInstallHint(dest string) string {
	switch runtime.GOOS {
	case "darwin":
		return "  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain " + dest
	default:
		return "  sudo cp " + dest + " /usr/local/share/ca-certificates/docktree-caddy.crt && sudo update-ca-certificates"
	}
}
