package compose

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrNoComposeFile = errors.New("no compose file found")

var defaultComposeFiles = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

var defaultOverrideFiles = []string{
	"compose.override.yaml",
	"compose.override.yml",
	"docker-compose.override.yaml",
	"docker-compose.override.yml",
}

func DiscoverFiles(wd string, env map[string]string, configured string) ([]string, error) {
	if composeFile := strings.TrimSpace(env["COMPOSE_FILE"]); composeFile != "" {
		return absolutizeFiles(wd, splitComposeFiles(composeFile)), nil
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		return absolutizeFiles(wd, splitComposeFiles(configured)), nil
	}

	var files []string
	for _, name := range defaultComposeFiles {
		path := filepath.Join(wd, name)
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
			break
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoComposeFile, wd)
	}

	for _, name := range defaultOverrideFiles {
		path := filepath.Join(wd, name)
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
			break
		}
	}
	return files, nil
}

func ConfigCommandArgv(files []string) []string {
	argv := []string{"docker", "compose"}
	for _, file := range files {
		argv = append(argv, "-f", file)
	}
	return append(argv, "config", "--no-interpolate", "--no-normalize", "--format", "json")
}

func splitComposeFiles(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == filepath.ListSeparator
	})
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			files = append(files, part)
		}
	}
	return files
}

func absolutizeFiles(wd string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		if filepath.IsAbs(file) {
			out = append(out, file)
		} else {
			out = append(out, filepath.Join(wd, file))
		}
	}
	return out
}
