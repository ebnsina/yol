package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// The router is configured over its own interface rather than by writing files and restarting
// it, so changing where a hostname points never interrupts what is already being served.
//
// The whole configuration is replaced each time rather than patched. It is small, and one shape
// that is always correct beats a set of partial updates that have to be applied in order.

const routerConfigTimeout = 20 * time.Second

// caddyConfig is the configuration sent to the router. Only the parts used are modelled, so a
// change in what is sent is visible here rather than hidden in a string.
type caddyConfig struct {
	Admin *caddyAdmin    `json:"admin,omitempty"`
	Apps  map[string]any `json:"apps"`
}

type caddyAdmin struct {
	Listen string `json:"listen"`
}

// applyRouterConfig tells the router which hostnames it serves and where each one goes.
func (a *Agent) applyRouterConfig(ctx context.Context, spec *proto.Spec) error {
	if spec.Router == nil {
		return nil // this server has no router, so there is nothing to configure
	}

	config, err := json.Marshal(buildRouterConfig(spec))
	if err != nil {
		return fmt.Errorf("agent: build router configuration: %w", err)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/load", spec.Router.AdminPort)

	reqCtx, cancel := context.WithTimeout(ctx, routerConfigTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(config))
	if err != nil {
		return fmt.Errorf("agent: configure router: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent: reach the router: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// The router explains what it rejected, and that explanation is worth keeping.
		body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("agent: the router refused the configuration: %s", bytes.TrimSpace(body))
	}
	return nil
}

// buildRouterConfig turns the specification into what the router expects.
func buildRouterConfig(spec *proto.Spec) caddyConfig {
	routes := make([]any, 0, len(spec.Routes))
	for _, route := range spec.Routes {
		routes = append(routes, map[string]any{
			"match": []any{map[string]any{"host": []string{route.Host}}},
			"handle": []any{map[string]any{
				"handler": "reverse_proxy",
				// Reached by name over the private network, so the app needs nothing published.
				"upstreams": []any{map[string]any{
					"dial": route.Container + ":" + strconv.Itoa(route.Port),
				}},
			}},
			"terminal": true,
		})
	}

	config := caddyConfig{
		Admin: &caddyAdmin{Listen: "0.0.0.0:" + strconv.Itoa(spec.Router.AdminPort)},
		Apps: map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"yol": map[string]any{
						"listen": []string{":80", ":443"},
						"routes": routes,
					},
				},
			},
		},
	}

	// Certificates are obtained as hostnames arrive rather than listed in advance, which is what
	// lets a custom domain start working without reconfiguring anything. The router asks the
	// control plane first, so pointing a name at a customer's server is not enough to make it
	// request a certificate.
	if spec.Router.PermissionURL != "" {
		config.Apps["tls"] = map[string]any{
			"automation": map[string]any{
				"on_demand": map[string]any{
					"permission": map[string]any{
						"module":   "http",
						"endpoint": spec.Router.PermissionURL,
					},
				},
				"policies": []any{map[string]any{"on_demand": true}},
			},
		}
	}
	return config
}

// syncRouter configures the router, waiting briefly for it to be ready. A router that has just
// been created is not answering yet, and failing on the first attempt would leave traffic
// unrouted until the next pass.
func (a *Agent) syncRouter(ctx context.Context, spec *proto.Spec) {
	if spec.Router == nil {
		return
	}

	var lastErr error
	for attempt := range 10 {
		if lastErr = a.applyRouterConfig(ctx, spec); lastErr == nil {
			if attempt > 0 {
				slog.Info("configured the router", "hostnames", len(spec.Routes), "attempts", attempt+1)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	slog.Error("could not configure the router", "error", lastErr)
}
