package identity

import "testing"

func TestDeriveNames(t *testing.T) {
	n := Derive("flexiple", "flexiple_platform")
	if n.Project != "flexiple-flexiple_platform" {
		t.Errorf("Project = %q", n.Project)
	}
	if n.InfraProject != "flexiple-infra" {
		t.Errorf("InfraProject = %q", n.InfraProject)
	}
	if n.SharedNetwork != "flexiple_shared" {
		t.Errorf("SharedNetwork = %q", n.SharedNetwork)
	}
	if n.InfraNetwork != n.SharedNetwork {
		t.Errorf("InfraNetwork legacy alias = %q, want %q", n.InfraNetwork, n.SharedNetwork)
	}
	if n.DevDB != "flexiple_flexiple_platform" {
		t.Errorf("DevDB = %q", n.DevDB)
	}
	if n.TestDB != "flexiple_flexiple_platform_test" {
		t.Errorf("TestDB = %q", n.TestDB)
	}
}

func TestValidateSlug(t *testing.T) {
	if err := ValidateSlug("flexiple_platform"); err != nil {
		t.Errorf("valid slug rejected: %v", err)
	}
	for _, bad := range []string{"has-dash", "Caps", "semi;colon", "", "space bar"} {
		if err := ValidateSlug(bad); err == nil {
			t.Errorf("ValidateSlug(%q) accepted a bad slug", bad)
		}
	}
}
