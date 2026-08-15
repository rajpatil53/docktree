// Package stateful owns pure helpers for Docktree's opt-in stateful forks.
package stateful

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	LabelManaged = "com.docktree.managed"
	LabelApp     = "com.docktree.app"
	LabelSlug    = "com.docktree.slug"
	LabelProject = "com.docktree.project"
	LabelService = "com.docktree.service"
	LabelFork    = "com.docktree.fork"
)

type ResourceKind string

const (
	ResourceVolume    ResourceKind = "volume"
	ResourceLogicalDB ResourceKind = "logical_db"
)

type DestroyRequest struct {
	App            string
	Slug           string
	MainSlug       string
	Service        string
	Strategy       string
	ResourceKind   ResourceKind
	ResourceName   string
	SnapshotSource string
	SourceDB       string
}

type Volume struct {
	Name    string
	App     string
	Slug    string
	Project string
	Service string
	Fork    string
	Labels  map[string]string
}

type volumeRecord struct {
	Name   string `json:"Name"`
	Labels any    `json:"Labels"`
}

var volumeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func CheckDestroy(req DestroyRequest) error {
	if req.Strategy != "isolated" {
		return fmt.Errorf("refusing to destroy %s %q for strategy %q", req.ResourceKind, req.ResourceName, req.Strategy)
	}
	if req.Slug == "" || req.Slug == req.MainSlug {
		return fmt.Errorf("refusing to destroy main/shared resource for slug %q", req.Slug)
	}
	want := ""
	switch req.ResourceKind {
	case ResourceVolume:
		want = VolumeName(req.App, req.Slug, req.Service)
		if req.SnapshotSource != "" && req.ResourceName == req.SnapshotSource {
			return fmt.Errorf("refusing to destroy shared snapshot source %q", req.ResourceName)
		}
	case ResourceLogicalDB:
		if req.SourceDB == "" {
			return fmt.Errorf("missing source_db for logical database destroy guard")
		}
		want = TargetDB(req.SourceDB, req.Slug)
		if req.ResourceName == req.SourceDB {
			return fmt.Errorf("refusing to destroy source logical database %q", req.SourceDB)
		}
	default:
		return fmt.Errorf("unknown resource kind %q", req.ResourceKind)
	}
	if req.ResourceName != want {
		return fmt.Errorf("refusing to destroy %q; only own isolated resource %q is allowed", req.ResourceName, want)
	}
	return nil
}

func VolumeName(app, slug, service string) string {
	return fmt.Sprintf("%s-%s-%sdata", app, slug, service)
}

func SnapshotSource(app, service, configured string) string {
	if configured != "" {
		return configured
	}
	return fmt.Sprintf("%s_%sdata", app, service)
}

func ValidateVolumeName(name string) error {
	if !volumeNameRe.MatchString(name) || strings.ContainsAny(name, `/\:`) {
		return fmt.Errorf("invalid Docker volume name %q", name)
	}
	return nil
}

func VolumeLabels(app, slug, project, service string) map[string]string {
	return map[string]string{
		LabelManaged: "true",
		LabelApp:     app,
		LabelSlug:    slug,
		LabelProject: project,
		LabelService: service,
		LabelFork:    service,
	}
}

func VolumeCreateArgv(app, slug, project, service string) []string {
	labels := VolumeLabels(app, slug, project, service)
	keys := []string{LabelManaged, LabelApp, LabelSlug, LabelProject, LabelService, LabelFork}
	argv := []string{"docker", "volume", "create"}
	for _, key := range keys {
		argv = append(argv, "--label", key+"="+labels[key])
	}
	return append(argv, VolumeName(app, slug, service))
}

func VolumeSnapshotArgv(src, dst string) []string {
	return []string{"docker", "run", "--rm", "--mount", "type=volume,src=" + src + ",dst=/from,readonly", "--mount", "type=volume,src=" + dst + ",dst=/to", "alpine", "sh", "-c", "cp -a /from/. /to/"}
}

func QuiesceWarning(source string) string {
	return fmt.Sprintf("stateful snapshot source %s must be quiesced before copying", source)
}

func IsForkVolume(labels map[string]string, service string) bool {
	fork := labels[LabelFork]
	if fork == "" {
		fork = labels["docktree.fork"]
	}
	if fork == "" {
		return false
	}
	return service == "" || fork == service
}

func ParseVolumeListJSON(data []byte) ([]Volume, error) {
	records, err := parseJSONRecords[volumeRecord](data)
	if err != nil {
		return nil, err
	}
	volumes := make([]Volume, 0, len(records))
	for _, record := range records {
		labels := parseLabels(record.Labels)
		volumes = append(volumes, Volume{
			Name:    record.Name,
			App:     first(labels, LabelApp, "docktree.app"),
			Slug:    first(labels, LabelSlug, "docktree.slug"),
			Project: first(labels, LabelProject, "docktree.project"),
			Service: first(labels, LabelService, "docktree.service"),
			Fork:    first(labels, LabelFork, "docktree.fork"),
			Labels:  labels,
		})
	}
	return volumes, nil
}

func (v Volume) IsForkFor(service string) bool {
	return IsForkVolume(v.Labels, service)
}

func SourceDB(source string) string {
	return source
}

func TargetDB(sourceDB, slug string) string {
	return sourceDB + "_" + slug
}

func UsePostgresFastPath(engine string) bool {
	switch strings.ToLower(engine) {
	case "postgres", "postgresql":
		return true
	default:
		return false
	}
}

func parseJSONRecords[T any](data []byte) ([]T, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var records []T
		if err := json.Unmarshal([]byte(trimmed), &records); err != nil {
			return nil, err
		}
		return records, nil
	}
	var records []T
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record T
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func parseLabels(value any) map[string]string {
	out := map[string]string{}
	switch labels := value.(type) {
	case map[string]any:
		for key, raw := range labels {
			out[key] = stringify(raw)
		}
	case string:
		for _, item := range strings.Split(labels, ",") {
			key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
			if ok && key != "" {
				out[key] = value
			}
		}
	}
	return out
}

func first(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := labels[key]; value != "" {
			return value
		}
	}
	return ""
}

func stringify(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}
