package cmd

import "testing"

func TestPrimarySlugFromCommonDir(t *testing.T) {
	cases := []struct{ commonDir, want string }{
		{"/Users/x/Codebase/flexiple-platform/.git", "flexiple_platform"},
		{"/tmp/acme/.git", "acme"},
		{"/srv/My-App/.git", "my_app"},
	}
	for _, c := range cases {
		if got := primarySlugFromCommonDir(c.commonDir); got != c.want {
			t.Errorf("primarySlugFromCommonDir(%q) = %q, want %q", c.commonDir, got, c.want)
		}
	}
}
