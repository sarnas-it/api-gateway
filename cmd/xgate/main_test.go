package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSpec(t *testing.T) {
	m, err := parseSpec("github.com/acme/plugin@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if m.Path != "github.com/acme/plugin" || m.Version != "v1.2.3" {
		t.Fatalf("spec = %+v", m)
	}

	m, err = parseSpec("github.com/acme/plugin=../fork")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(m.Replacement) || !strings.HasSuffix(m.Replacement, "fork") {
		t.Fatalf("replacement = %q, want absolute .../fork", m.Replacement)
	}

	if _, err := parseSpec(""); err == nil {
		t.Fatal("empty spec must fail")
	}
}

func TestRenderMain(t *testing.T) {
	got := renderMain("code.sarnas.ru/api-gateway", []moduleSpec{
		{Path: "github.com/acme/plugin", Version: "v1.0.0"},
	})
	for _, want := range []string{
		`"code.sarnas.ru/api-gateway/pkg/app"`,
		`_ "code.sarnas.ru/api-gateway/plugins/standard"`,
		`_ "github.com/acme/plugin"`,
		`app.Main()`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderMain missing %q:\n%s", want, got)
		}
	}
}

func TestModuleRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/gw\n\ngo 1.26.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gotRoot, gotModule, err := moduleRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root || gotModule != "example.com/gw" {
		t.Fatalf("root=%q module=%q", gotRoot, gotModule)
	}
}
