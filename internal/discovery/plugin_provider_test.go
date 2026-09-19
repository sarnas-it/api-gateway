package discovery

import (
	"testing"

	"github.com/basili4-1982/api-gateway/internal/config"
)

func TestResultProtoRoundtrip(t *testing.T) {
	in := Result{
		Targets: []config.TargetConfig{{Name: "a", URL: "http://x:1", PathPrefix: "/api", StripPrefix: true}},
		Rules:   []config.RoutingRule{{Host: "h", PathPrefix: "/", TargetName: "a"}},
	}
	msg := resultToProto(in)
	out := resultFromProto(msg)
	if len(out.Targets) != 1 || out.Targets[0].Name != "a" {
		t.Fatalf("targets mismatch: %+v", out.Targets)
	}
	if len(out.Rules) != 1 || out.Rules[0].TargetName != "a" {
		t.Fatalf("rules mismatch: %+v", out.Rules)
	}
}
