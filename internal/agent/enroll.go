package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// enroll trades the single-use token written during setup for a lasting credential.
func (a *Agent) enroll(ctx context.Context, enrollmentToken string) (string, error) {
	url := strings.TrimSuffix(a.cfg.ControlPlaneURL.String(), "/") + "/v1/agent/enroll"

	body, err := json.Marshal(map[string]string{"enrollmentToken": enrollmentToken})
	if err != nil {
		return "", fmt.Errorf("agent: build enrollment request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("agent: build enrollment request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent: reach the control plane: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// Usually an expired or already-used token, which no amount of retrying will fix.
		return "", fmt.Errorf("agent: enrollment refused with status %d", res.StatusCode)
	}

	var out struct {
		AgentToken string `json:"agentToken"`
		ServerID   string `json:"serverId"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("agent: read enrollment answer: %w", err)
	}
	if out.AgentToken == "" {
		return "", fmt.Errorf("agent: enrollment returned no credential")
	}
	return out.AgentToken, nil
}
