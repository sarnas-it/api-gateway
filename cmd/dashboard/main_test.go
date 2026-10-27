package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"code.sarnas.ru/api-gateway/internal/dashboard"
)

func TestRunGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv := dashboard.NewServer(dashboard.ServerConfig{})
	var out bytes.Buffer
	if err := run(ctx, "127.0.0.1:0", srv, &out); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(out.String(), "listening") {
		t.Errorf("output = %q, want a listening message", out.String())
	}
}

func TestRunListenError(t *testing.T) {
	srv := dashboard.NewServer(dashboard.ServerConfig{})
	var out bytes.Buffer
	if err := run(context.Background(), "invalid-address", srv, &out); err == nil {
		t.Fatal("run() error = nil, want listen error")
	}
	if strings.Contains(out.String(), "listening") {
		t.Errorf("output = %q, must not log listening before a successful bind", out.String())
	}
}

func TestValidateBasicAuth(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "empty allowed", in: "", wantErr: false},
		{name: "user and password", in: "admin:secret", wantErr: false},
		{name: "empty password allowed", in: "admin:", wantErr: false},
		{name: "missing colon rejected", in: "admin", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBasicAuth(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBasicAuth(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
		})
	}
}
