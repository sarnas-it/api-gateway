package plugin

import (
	"net/http"
	"testing"
)

type connectorModule struct{}

func (connectorModule) Name() string { return "test.connector" }
func (connectorModule) Authenticate(r *http.Request) (map[string]any, error) {
	return map[string]any{"id": "svc"}, nil
}

func TestAuthConnectors(t *testing.T) {
	Register(connectorModule{})
	connectors := AuthConnectors()
	c, ok := connectors["test.connector"]
	if !ok {
		t.Fatalf("connector not registered: %v", connectors)
	}
	claims, err := c.Authenticate(nil)
	if err != nil || claims["id"] != "svc" {
		t.Fatalf("claims=%v err=%v", claims, err)
	}
	if _, ok := connectors["example"]; ok {
		t.Fatal("middleware module must not be registered as auth connector")
	}
}
