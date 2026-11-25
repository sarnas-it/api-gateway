package app

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "check-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

const validConfig = `
targets:
  - name: backend
    url: "http://backend:80"
routing:
  rules:
    - path_prefix: "/"
      target_name: "backend"
`

func TestRunCheck_Valid(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, validConfig), false, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "config OK") {
		t.Fatalf("stdout = %q, want config OK", out.String())
	}
}

func TestRunCheck_Invalid(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, "targets: []\n"), false, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "config invalid") {
		t.Fatalf("stderr = %q, want config invalid", errOut.String())
	}
}

func TestRunCheck_UnknownKeyWarns(t *testing.T) {
	cfg := validConfig + "bogus_key: 1\n"
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, cfg), false, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "unknown config key: bogus_key") {
		t.Fatalf("stderr = %q, want unknown key warning", errOut.String())
	}
}

func TestRunCheck_StrictFailsOnUnknownKey(t *testing.T) {
	cfg := validConfig + "bogus_key: 1\n"
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, cfg), true, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

// env: dev без headers.cors — предупреждение, но не ошибка.
func TestRunCheck_DevWithoutCORSWarns(t *testing.T) {
	cfg := "application:\n  env: \"dev\"\n" + validConfig
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, cfg), false, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "CORS") || !strings.Contains(errOut.String(), "headers.cors") {
		t.Fatalf("stderr = %q, want CORS warning pointing to headers.cors", errOut.String())
	}
}

// -strict относится к неизвестным ключам, а не к CORS-предупреждению.
func TestRunCheck_DevWithoutCORSStrictStillOK(t *testing.T) {
	cfg := "application:\n  env: \"dev\"\n" + validConfig
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, cfg), true, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
}

// С явно включённой секцией cors предупреждения нет.
func TestRunCheck_DevWithCORSDoesNotWarn(t *testing.T) {
	cfg := "application:\n  env: \"dev\"\nheaders:\n  cors:\n    enabled: true\n    allowed_origins: [\"https://app.example\"]\n" + validConfig
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, cfg), false, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), "CORS") {
		t.Fatalf("stderr = %q, want no CORS warning", errOut.String())
	}
}

func TestRunCheck_MissingEnvFails(t *testing.T) {
	os.Unsetenv("CHECK_MISSING_ENV")
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, validConfig+"jwt:\n  secret_key: \"${CHECK_MISSING_ENV}\"\n"), false, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "CHECK_MISSING_ENV") {
		t.Fatalf("stderr = %q, want missing variable name", errOut.String())
	}
}

func TestRunCheck_UnknownPlugin(t *testing.T) {
	cfg := validConfig + "plugins:\n  no.such.module:\n    a: b\n"
	var out, errOut strings.Builder
	code := runCheck(writeTemp(t, cfg), false, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "no.such.module") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunCheck_UnknownAuthConnector(t *testing.T) {
	cfg := `
targets:
  - name: backend
    url: "http://backend:80"
routing:
  rules:
    - path_prefix: "/api/keys"
      target_name: "backend"
      auth:
        connector: "no-such-connector"
`
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, cfg), false, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "no-such-connector") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestRunCheck_UnknownDiscoveryModule(t *testing.T) {
	cfg := `
discovery:
  enabled: true
  module: "no-such-discovery"
`
	var out, errOut bytes.Buffer
	code := runCheck(writeTemp(t, cfg), false, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "no-such-discovery") {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}
