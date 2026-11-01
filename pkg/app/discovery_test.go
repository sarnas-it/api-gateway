package app

import (
	"context"
	"reflect"
	"testing"

	"code.sarnas.ru/api-gateway/internal/config"
	"code.sarnas.ru/api-gateway/pkg/plugin"
)

type fakeDiscovery struct{}

func (fakeDiscovery) Name() string { return "test.discovery" }
func (fakeDiscovery) Watch(ctx context.Context, emit func([]plugin.DiscoveredService)) error {
	return nil
}

func TestServicesToResult(t *testing.T) {
	res := servicesToResult([]plugin.DiscoveredService{
		{Name: "api-1", Address: "10.0.0.5:8080", PathPrefix: "/api", StripPath: true},
		{Name: "api-2", Address: "https://api2.internal"},
		{Name: "", Address: "1.1.1.1:1"},          // без имени — пропуск
		{Name: "api-3", Address: ""},              // без адреса — пропуск
		{Name: "api-1", Address: "10.0.0.6:8080"}, // дубликат имени — пропуск
	}, 0, nil)

	if len(res.Targets) != 2 {
		t.Fatalf("targets = %+v", res.Targets)
	}
	if res.Targets[0].URL != "http://10.0.0.5:8080" {
		t.Fatalf("target[0].URL = %q", res.Targets[0].URL)
	}
	if res.Targets[1].URL != "https://api2.internal" {
		t.Fatalf("target[1].URL = %q", res.Targets[1].URL)
	}
	if res.Targets[0].Timeout <= 0 {
		t.Fatalf("timeout not defaulted: %+v", res.Targets[0])
	}
	want := []string{"/api|api-1", "/api-2|api-2"}
	var got []string
	for _, r := range res.Rules {
		got = append(got, r.PathPrefix+"|"+r.TargetName)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	if !res.Rules[0].StripPath || res.Rules[1].StripPath {
		t.Fatalf("strip_path = %+v", res.Rules)
	}
}

func TestValidateDiscoveryModule(t *testing.T) {
	disabled := &config.Config{Discovery: &config.DiscoveryConfig{Module: "missing"}}
	if err := validateDiscoveryModule(disabled); err != nil {
		t.Fatalf("disabled discovery must not validate module: %v", err)
	}

	missing := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, Module: "missing"}}
	if err := validateDiscoveryModule(missing); err == nil {
		t.Fatal("unknown module must fail")
	}

	plugin.Register(fakeDiscovery{})
	known := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, Module: "test.discovery"}}
	if err := validateDiscoveryModule(known); err != nil {
		t.Fatalf("compiled module must pass: %v", err)
	}
}
