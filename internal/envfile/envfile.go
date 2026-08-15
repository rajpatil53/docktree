// Package envfile is the single writer of the per-worktree
// .docktree/.env.worktree, and the reader used by docktree diagnostics to report
// current state.
package envfile

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Artifact struct {
	ProjectName     string
	SharedNetwork   string
	WorktreeNetwork string
	Ports           map[string]int
	Env             map[string]string
}

func Pairs(a Artifact) [][2]string {
	kv := [][2]string{
		{"COMPOSE_PROJECT_NAME", a.ProjectName},
		{"DOCKTREE_SHARED_NETWORK", a.SharedNetwork},
	}
	if a.WorktreeNetwork != "" {
		kv = append(kv, [2]string{"DOCKTREE_WORKTREE_NETWORK", a.WorktreeNetwork})
	}
	for _, key := range sortedIntKeys(a.Ports) {
		kv = append(kv, [2]string{key, strconv.Itoa(a.Ports[key])})
	}
	for _, key := range sortedStringKeys(a.Env) {
		kv = append(kv, [2]string{key, a.Env[key]})
	}
	return kv
}

func WriteArtifact(path string, a Artifact) error {
	return Write(path, Pairs(a))
}

// Write emits ordered KEY=VALUE lines atomically (temp+rename).
func Write(path string, kv [][2]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b []byte
	for _, p := range kv {
		b = append(b, []byte(p[0]+"="+p[1]+"\n")...)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Read parses simple KEY=VALUE env files into a map. Blank lines and lines
// without '=' are skipped; values are taken verbatim after the first '='. A
// missing file yields an empty map.
func Read(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		out[k] = v
	}
	return out, nil
}

func sortedIntKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
