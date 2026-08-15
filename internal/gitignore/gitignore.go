package gitignore

import (
	"os"
	"path/filepath"
	"strings"
)

const DocktreeEntry = ".docktree/"

func EnsureDocktree(root string) (bool, error) {
	return EnsureEntry(filepath.Join(root, ".gitignore"), DocktreeEntry)
}

func EnsureEntry(path, entry string) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true, writeAtomic(path, []byte(entry+"\n"))
	}
	if err != nil {
		return false, err
	}
	if hasEntry(string(data), entry) {
		return false, nil
	}
	next := string(data)
	if next != "" && !strings.HasSuffix(next, "\n") {
		next += "\n"
	}
	next += entry + "\n"
	return true, writeAtomic(path, []byte(next))
}

func hasEntry(data, entry string) bool {
	entry = strings.TrimSpace(entry)
	for _, line := range strings.Split(data, "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
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
