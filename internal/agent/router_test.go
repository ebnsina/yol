package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ebnsina/yol/internal/proto"
)

func TestRouterConfigMapsHostnamesToContainers(t *testing.T) {
	spec := &proto.Spec{
		Router: &proto.SpecRouter{AdminPort: 2019, PermissionURL: "https://control/v1/tls/allow"},
		Routes: []proto.SpecRoute{
			{Host: "app.example.com", Container: "yol-abc123", Port: 3000},
			{Host: "staging.example.com", Container: "yol-def456", Port: 8080},
		},
	}

	encoded, err := json.Marshal(buildRouterConfig(spec))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	config := string(encoded)

	for _, expected := range []string{
		`"host":["app.example.com"]`,
		`"dial":"yol-abc123:3000"`,
		`"host":["staging.example.com"]`,
		`"dial":"yol-def456:8080"`,
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("configuration is missing %s:\n%s", expected, config)
		}
	}
}

// Certificates must only ever be obtained after asking, which is what stops anyone pointing a
// hostname at a customer's server and making it request one.
func TestRouterConfigAlwaysAsksBeforeObtainingCertificates(t *testing.T) {
	spec := &proto.Spec{
		Router: &proto.SpecRouter{AdminPort: 2019, PermissionURL: "https://control/v1/tls/allow"},
	}

	encoded, _ := json.Marshal(buildRouterConfig(spec))
	config := string(encoded)

	if !strings.Contains(config, `"permission"`) {
		t.Error("no permission check was configured, so certificates could be requested freely")
	}
	if !strings.Contains(config, "https://control/v1/tls/allow") {
		t.Error("the permission endpoint was not included")
	}
	if !strings.Contains(config, `"on_demand":true`) {
		t.Error("certificates were not set to be obtained as hostnames arrive")
	}
}

// Without somewhere to ask, obtaining certificates must not be enabled at all. The router itself
// refuses this combination, so producing it would break the whole configuration.
func TestRouterConfigOmitsCertificatesWithNowhereToAsk(t *testing.T) {
	spec := &proto.Spec{Router: &proto.SpecRouter{AdminPort: 2019}}

	encoded, _ := json.Marshal(buildRouterConfig(spec))
	if strings.Contains(string(encoded), "on_demand") {
		t.Error("certificates were enabled with no permission endpoint, which the router rejects")
	}
}

func TestRouterConfigListensOnWebPorts(t *testing.T) {
	spec := &proto.Spec{Router: &proto.SpecRouter{AdminPort: 2019}}

	encoded, _ := json.Marshal(buildRouterConfig(spec))
	config := string(encoded)

	if !strings.Contains(config, `":80"`) || !strings.Contains(config, `":443"`) {
		t.Errorf("the router is not listening on both web ports:\n%s", config)
	}
	if !strings.Contains(config, `"0.0.0.0:2019"`) {
		t.Error("the control interface is not reachable, so it could never be configured")
	}
}

// A server with no hostnames yet must still produce a valid configuration, so the router runs
// and is ready before the first domain is added.
func TestRouterConfigIsValidWithNoHostnames(t *testing.T) {
	spec := &proto.Spec{Router: &proto.SpecRouter{AdminPort: 2019}}

	encoded, err := json.Marshal(buildRouterConfig(spec))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back map[string]any
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("the configuration is not valid: %v", err)
	}
	if _, ok := back["apps"]; !ok {
		t.Error("no applications were configured")
	}
}
