package identity

import (
	"fmt"
	"regexp"
)

// Names holds every identifier derived from (app, slug). DevDB/TestDB use the
// app's db name_template at call sites; the defaults here match flexiple.
type Names struct {
	App           string
	Slug          string
	Project       string // per-worktree compose project
	InfraProject  string // shared infra compose project
	SharedNetwork string // external network shared infra and worktree stacks join
	InfraNetwork  string // legacy alias for SharedNetwork until older commands retire
	DevDB         string
	TestDB        string
	Namespace     string // e.g. OPENSEARCH_INDEX_PREFIX token "<slug>_"
}

// Derive builds the default names. DevDB/TestDB follow "<app>_<slug>(_test)";
// callers with a custom db.name_template override DevDB/TestDB afterward.
func Derive(app, slug string) Names {
	return Names{
		App:           app,
		Slug:          slug,
		Project:       app + "-" + slug,
		InfraProject:  app + "-infra",
		SharedNetwork: app + "_shared",
		InfraNetwork:  app + "_shared",
		DevDB:         app + "_" + slug,
		TestDB:        app + "_" + slug + "_test",
		Namespace:     slug + "_",
	}
}

var slugRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// ValidateSlug refuses anything that could be unsafe in raw SQL / docker args.
// Mirrors bin/wt-remove.sh:53.
func ValidateSlug(slug string) error {
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("refusing to act on suspicious slug %q (must match ^[a-z0-9_]+$)", slug)
	}
	return nil
}
