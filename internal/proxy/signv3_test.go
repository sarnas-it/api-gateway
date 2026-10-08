package proxy

import "testing"

func TestSignV3(t *testing.T) {
	sig, ts := signV3("admin-front", "42", "GET", "/api/v1/jobs", "test-secret", 1700000000)
	if ts != "1700000000" {
		t.Fatalf("timestamp = %q", ts)
	}
	want := "1e32df9bf01690ed3f33ec62d61f32abe2cdc1488707fbf15ffca111e5ff670a"
	if sig != want {
		t.Fatalf("signature = %q, want %q", sig, want)
	}
	// метод регистронезависим (UPPER внутри формулы)
	sigUpper, _ := signV3("admin-front", "42", "get", "/api/v1/jobs", "test-secret", 1700000000)
	if sigUpper != want {
		t.Fatalf("method case sensitivity: %q", sigUpper)
	}
}
