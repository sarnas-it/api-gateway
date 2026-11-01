package route53

import "testing"

func TestProviderConfig(t *testing.T) {
	p := provider{}
	a, d, err := p.New(map[string]string{
		"region":            "eu-central-1",
		"access_key_id":     "key",
		"secret_access_key": "secret",
		"max_retries":       "3",
	})
	if err != nil || a == nil || d == nil {
		t.Fatalf("New = %v, %v", a, err)
	}
}
