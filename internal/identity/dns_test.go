package identity

import "testing"

func TestDNSLabel(t *testing.T) {
	cases := map[string]string{
		"feature_x": "feature-x",
		"Feature_X": "feature-x",
		"shop":      "shop",
		"a_b_c":     "a-b-c",
		"main":      "main",
		"_edge_":    "edge",
		"my.app":    "my-app",
	}
	for in, want := range cases {
		if got := DNSLabel(in); got != want {
			t.Fatalf("DNSLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
