package desec

import "testing"

func TestProvider(t *testing.T) {
	p := provider{}
	a, d, err := p.New(map[string]string{"token": "t"})
	if err != nil || a == nil || d == nil {
		t.Fatalf("New = %v, %v", a, err)
	}
	t.Setenv("DESEC_TOKEN", "")
	if _, _, err := p.New(nil); err == nil {
		t.Fatal("expected error without token")
	}
}
