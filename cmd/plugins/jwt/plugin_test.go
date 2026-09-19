package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/sarnas-it/pluginrpc"
)

func buildSubprocess(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "jwt")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	return bin
}

func TestJWTServerValidate(t *testing.T) {
	bin := buildSubprocess(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h, err := pluginrpc.Start(ctx, pluginrpc.Config{
		Path:         bin,
		Name:         "jwt",
		Transport:    pluginrpc.TransportGRPC,
		PluginConfig: config.JWTConfig{SecretKey: "benchsecret", Algorithm: "HS256", ValidateExp: true, ClaimMappings: []string{"id", "email"}},
		StartTimeout: 10 * time.Second,
		StopTimeout:  3 * time.Second,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	})
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer h.Stop(context.Background())

	client := authv1.NewAuthServiceClient(h.Conn())
	resp, err := client.Validate(ctx, &authv1.ValidateRequest{Token: "not-a-token", Required: true})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if resp.GetOk() {
		t.Fatal("expected invalid token to be rejected")
	}
}
