package config

import "testing"

func TestIdentityProviderSubjectDefaults(t *testing.T) {
	c := &Config{}
	c.Identity.Providers = []IdentityProviderConfig{{Name: "partner"}}
	c.setDefaults()

	if c.Identity.Providers[0].SubjectClaim != "sub" {
		t.Fatalf("expected default subject_claim sub, got %q", c.Identity.Providers[0].SubjectClaim)
	}
	if c.Identity.Providers[0].SubjectProvider != "" {
		t.Fatalf("expected empty subject_provider by default, got %q", c.Identity.Providers[0].SubjectProvider)
	}
}
