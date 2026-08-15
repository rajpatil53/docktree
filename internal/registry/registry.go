// Package registry owns ~/.config/docktree/registry.json: soft per-app
// port-band and stable shared-port reservations.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// BandStart is the first base docktree hands out when seeding is not provided.
const BandStart = 20000
const BandWidth = 1000

type App struct {
	Bands       map[string]int `json:"bands,omitempty"`        // service -> band base
	SharedPorts map[string]int `json:"shared_ports,omitempty"` // shared service -> host port
}

type file struct {
	Apps map[string]App `json:"apps"`
}

// EnsureApp returns the app's service bands, allocating any missing seeded
// service reservations on first use. The read-modify-write runs under an
// exclusive per-app flock and the registry save is atomic.
func EnsureApp(path, app string, seed map[string]int) (App, error) {
	return withAppLock(path, app, func() (App, error) {
		f, err := load(path)
		if err != nil {
			return App{}, err
		}

		current := normalizeApp(f.Apps[app])
		changed := false
		if _, ok := f.Apps[app]; !ok {
			changed = true
		}
		used := usedBands(f)
		for _, name := range sortedKeys(seed) {
			if _, ok := current.Bands[name]; ok {
				continue
			}
			preferred := seed[name]
			current.Bands[name] = reserveBand(used, preferred)
			changed = true
		}
		f.Apps[app] = current
		if changed {
			if err := save(path, f); err != nil {
				return App{}, err
			}
		}
		return current, nil
	})
}

// EnsureSharedPorts returns stable shared-service host ports for an app. New
// services prefer their canonical container port and then band-shift by
// BandWidth until the reservation is unused and currently bindable.
func EnsureSharedPorts(path, app string, canonical map[string]int, free func(int) bool) (map[string]int, error) {
	if free == nil {
		free = func(int) bool { return true }
	}
	return withAppLock(path, app, func() (map[string]int, error) {
		f, err := load(path)
		if err != nil {
			return nil, err
		}

		current := normalizeApp(f.Apps[app])
		changed := false
		if _, ok := f.Apps[app]; !ok {
			changed = true
		}
		used := usedSharedPorts(f)
		for _, name := range sortedKeys(canonical) {
			if _, ok := current.SharedPorts[name]; ok {
				continue
			}
			port, err := reserveSharedPort(used, canonical[name], free)
			if err != nil {
				return nil, fmt.Errorf("reserve shared port for %s: %w", name, err)
			}
			current.SharedPorts[name] = port
			changed = true
		}
		f.Apps[app] = current
		if changed {
			if err := save(path, f); err != nil {
				return nil, err
			}
		}
		return copyInts(current.SharedPorts), nil
	})
}

func withAppLock[T any](path, app string, fn func() (T, error)) (T, error) {
	var zero T
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return zero, err
	}
	lock, err := os.OpenFile(LockPath(path, app), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return zero, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return zero, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	return fn()
}

// LockPath returns the per-app lock file path used for registry updates and the
// later resolve/up critical section.
func LockPath(path, app string) string {
	app = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-' || r == '.':
			return r
		default:
			return '_'
		}
	}, app)
	if app == "" {
		app = "default"
	}
	return filepath.Join(filepath.Dir(path), filepath.Base(path)+"."+app+".lock")
}

func normalizeApp(app App) App {
	if app.Bands == nil {
		app.Bands = map[string]int{}
	}
	if app.SharedPorts == nil {
		app.SharedPorts = map[string]int{}
	}
	return app
}

func usedBands(f file) map[int]bool {
	used := map[int]bool{}
	for _, a := range f.Apps {
		for _, base := range a.Bands {
			used[base] = true
		}
	}
	return used
}

func reserveBand(used map[int]bool, preferred int) int {
	if preferred >= BandStart && !used[preferred] {
		used[preferred] = true
		return preferred
	}
	cand := BandStart
	for used[cand] {
		cand += BandWidth
	}
	used[cand] = true
	return cand
}

func usedSharedPorts(f file) map[int]bool {
	used := map[int]bool{}
	for _, a := range f.Apps {
		for _, port := range a.SharedPorts {
			used[port] = true
		}
	}
	return used
}

func reserveSharedPort(used map[int]bool, canonical int, free func(int) bool) (int, error) {
	if canonical <= 0 {
		return 0, fmt.Errorf("canonical port must be positive")
	}
	for port := canonical; port <= 65535; port += BandWidth {
		if used[port] || !free(port) {
			continue
		}
		used[port] = true
		return port, nil
	}
	return 0, fmt.Errorf("no free shifted port for canonical port %d", canonical)
}

func load(path string) (file, error) {
	f := file{Apps: map[string]App{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("read registry %s: %w", path, err)
	}
	if f.Apps == nil {
		f.Apps = map[string]App{}
	}
	for app, current := range f.Apps {
		f.Apps[app] = normalizeApp(current)
	}
	return f, nil
}

func save(path string, f file) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func copyInts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
