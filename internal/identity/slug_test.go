package identity

import (
	"fmt"
	"hash/fnv"
	"testing"
)

func TestComputeSlug(t *testing.T) {
	long := ""
	for i := 0; i < 45; i++ {
		long += "a"
	}
	longWant := fmt.Sprintf("%s_%08x", long[:30], fnv32a(long))
	cases := []struct{ in, want string }{
		{"flexiple-platform", "flexiple_platform"}, // '-' -> '_'
		{"feat-my-feature", "feat_my_feature"},
		{"Feat-My-Feature", "feat_my_feature"}, // uppercase lowercases
		{"___trim-me___", "trim_me"},           // leading/trailing separators are stripped
		{"", "main"},                           // empty -> main
		{long, longWant},                       // 30 chars + '_' + 8-hex FNV-1a suffix
	}
	for _, c := range cases {
		if got := ComputeSlug(c.in); got != c.want {
			t.Errorf("ComputeSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComputeSlugWithInjectedHash(t *testing.T) {
	got := ComputeSlugWithHash("this-branch-name-is-long-enough-to-need-a-suffix", func(string) uint32 {
		return 0x1234abcd
	})
	if got != "this_branch_name_is_long_enoug_1234abcd" {
		t.Fatalf("ComputeSlugWithHash = %q", got)
	}
}

func TestAppName(t *testing.T) {
	if got := AppName("Configured-App", "/repos/ignored"); got != "configured_app" {
		t.Fatalf("explicit app = %q, want configured_app", got)
	}
	if got := AppName("", "/repos/My-App"); got != "my_app" {
		t.Fatalf("fallback app = %q, want my_app", got)
	}
}

func fnv32a(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}
