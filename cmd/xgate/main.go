// Command xgate builds a gateway binary with selected compile-time modules.
// It is the gateway's analogue of xcaddy: generate a main package with blank
// imports, resolve modules, and run go build.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const version = "0.1.0"

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "build":
		buildCmd(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Printf("xgate %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "xgate: unknown command %q\n", os.Args[1])
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `xgate — build a gateway with compile-time modules.

Usage:
  xgate build [flags]
  xgate version

Flags:
  --src <dir>       gateway source root (default ".")
  --output <file>   output binary (default "xgate-gateway")
  --with <spec>     module[@version][=replacement]; repeatable
  --tags <tags>     extra go build tags
  --keep            keep the temporary build directory
`)
	os.Exit(2)
}

func buildCmd(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	src := fs.String("src", ".", "gateway source root")
	output := fs.String("output", "xgate-gateway", "output binary")
	tags := fs.String("tags", "", "extra go build tags")
	keep := fs.Bool("keep", false, "keep temporary build directory")
	var with stringList
	fs.Var(&with, "with", "module[@version][=replacement]")
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "xgate: unexpected arguments: %v\n", fs.Args())
		os.Exit(2)
	}

	root, module, err := moduleRoot(*src)
	if err != nil {
		fatal(err)
	}
	if err := build(root, module, *output, with, *tags, *keep); err != nil {
		fatal(err)
	}
}

func build(root, module, output string, specs []string, tags string, keep bool) error {
	dir, err := os.MkdirTemp("", "xgate-build-*")
	if err != nil {
		return err
	}
	if !keep {
		defer os.RemoveAll(dir)
	} else {
		fmt.Printf("xgate: build directory: %s\n", dir)
	}

	mods, err := parseSpecs(specs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(renderMain(module, mods)), 0o644); err != nil {
		return err
	}
	goVersion, err := moduleGoVersion(root)
	if err != nil {
		return err
	}
	gomod := fmt.Sprintf("module xgate-build\n\ngo %s\n\nrequire %s v0.0.0\n\nreplace %s => %s\n",
		goVersion, module, module, root)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		return err
	}

	for _, m := range mods {
		if m.Replacement != "" {
			if err := run(dir, "go", "mod", "edit", "-replace", m.Path+"="+m.Replacement); err != nil {
				return err
			}
		}
		if m.Version != "" {
			if err := run(dir, "go", "get", m.Path+"@"+m.Version); err != nil {
				return err
			}
		}
	}
	if err := run(dir, "go", "mod", "tidy"); err != nil {
		return err
	}

	out, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	args := []string{"build", "-trimpath", "-o", out}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, ".")
	if err := run(dir, "go", args...); err != nil {
		return err
	}
	fmt.Printf("xgate: built %s\n", out)
	for _, m := range mods {
		fmt.Printf("  module %s %s\n", m.Path, m.Version)
	}
	return nil
}

type moduleSpec struct {
	Path        string
	Version     string
	Replacement string
}

func parseSpecs(specs []string) ([]moduleSpec, error) {
	out := make([]moduleSpec, 0, len(specs))
	for _, raw := range specs {
		m, err := parseSpec(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func parseSpec(raw string) (moduleSpec, error) {
	left, replacement, _ := strings.Cut(raw, "=")
	if left == "" {
		return moduleSpec{}, fmt.Errorf("empty --with module")
	}
	path, version := left, ""
	if i := strings.LastIndex(left, "@"); i >= 0 {
		path, version = left[:i], left[i+1:]
	}
	if path == "" {
		return moduleSpec{}, fmt.Errorf("invalid --with %q", raw)
	}
	if replacement != "" {
		abs, err := filepath.Abs(replacement)
		if err != nil {
			return moduleSpec{}, err
		}
		replacement = abs
	}
	return moduleSpec{Path: path, Version: version, Replacement: replacement}, nil
}

func renderMain(module string, mods []moduleSpec) string {
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	fmt.Fprintf(&b, "\t%q\n", module+"/pkg/app")
	fmt.Fprintf(&b, "\t_ %q\n", module+"/plugins/standard")
	for _, m := range mods {
		fmt.Fprintf(&b, "\t_ %q\n", m.Path)
	}
	b.WriteString(")\n\nfunc main() {\n\tapp.Main()\n}\n")
	return b.String()
}

func moduleRoot(src string) (string, string, error) {
	dir, err := filepath.Abs(src)
	if err != nil {
		return "", "", err
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "module ") {
					return dir, strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
				}
			}
			return "", "", fmt.Errorf("%s/go.mod has no module directive", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("go.mod not found above %s", src)
		}
		dir = parent
	}
}

func moduleGoVersion(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "go ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "go ")), nil
		}
	}
	return "1.26", nil
}

func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "xgate: %v\n", err)
	os.Exit(1)
}
