package cloudflare

import "testing"

func TestProviderFromConfig(t *testing.T) {
	p := provider{}
	appender, deleter, err := p.New(map[string]string{"api_token": "token"})
	if err != nil {
		t.Fatal(err)
	}
	if appender == nil || deleter == nil {
		t.Fatal("provider returned nil manager")
	}
}

func TestProviderRequiresToken(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "")
	p := provider{}
	if _, _, err := p.New(nil); err == nil {
		t.Fatal("expected error without api_token")
	}
}
