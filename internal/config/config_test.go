package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validAPIEnv() map[string]string {
	return map[string]string{
		"YOL_ENV":              "development",
		"YOL_HTTP_ADDR":        ":8080",
		"YOL_PUBLIC_URL":       "http://localhost:8080",
		"YOL_DATABASE_URL":     "postgres://yol:yol@localhost:5433/yol?sslmode=disable",
		"YOL_SESSION_SECRET":   strings.Repeat("ab", 32),
		"YOL_SECRETS_KEY":      strings.Repeat("cd", 32),
		"YOL_AGENT_SPEC_KEY":   strings.Repeat("ef", 32),
		"YOL_SHUTDOWN_TIMEOUT": "15s",
	}
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestLoadAPIValid(t *testing.T) {
	setEnv(t, validAPIEnv())

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	if cfg.Env != EnvDevelopment {
		t.Errorf("Env = %q, want development", cfg.Env)
	}
	if cfg.Env.IsProduction() {
		t.Error("IsProduction() = true for development")
	}
	if cfg.PublicURL.Host != "localhost:8080" {
		t.Errorf("PublicURL.Host = %q, want localhost:8080", cfg.PublicURL.Host)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 15s", cfg.ShutdownTimeout)
	}
	if len(cfg.SecretsKey) != 32 {
		t.Errorf("SecretsKey length = %d, want 32", len(cfg.SecretsKey))
	}
}

// A missing key must fail rather than silently fall back to a default.
func TestLoadAPIMissingKeyFails(t *testing.T) {
	for key := range validAPIEnv() {
		t.Run(key, func(t *testing.T) {
			env := validAPIEnv()
			delete(env, key)
			setEnv(t, env)
			t.Setenv(key, "")

			_, err := LoadAPI()
			if err == nil {
				t.Fatalf("%s missing but config loaded", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error does not name %s: %v", key, err)
			}
		})
	}
}

func TestLoadAPIReportsEveryProblemAtOnce(t *testing.T) {
	env := validAPIEnv()
	env["YOL_ENV"] = "nonsense"
	env["YOL_PUBLIC_URL"] = "not a url at all"
	env["YOL_SHUTDOWN_TIMEOUT"] = "soon"
	setEnv(t, env)

	_, err := LoadAPI()
	if err == nil {
		t.Fatal("expected error")
	}
	var cfgErr *Error
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if len(cfgErr.Problems) != 3 {
		t.Errorf("Problems = %d (%v), want 3", len(cfgErr.Problems), cfgErr.Problems)
	}
}

func TestLoadAPIRejectsBadValues(t *testing.T) {
	cases := []struct {
		key, value, want string
	}{
		{"YOL_ENV", "prod", "must be one of"},
		{"YOL_PUBLIC_URL", "ftp://example.com", "must use http or https"},
		{"YOL_PUBLIC_URL", "https://", "must include a host"},
		{"YOL_SHUTDOWN_TIMEOUT", "-5s", "must be positive"},
		{"YOL_SHUTDOWN_TIMEOUT", "fifteen", "not a valid duration"},
		{"YOL_SECRETS_KEY", "zzzz", "must be hex-encoded"},
		{"YOL_SECRETS_KEY", "abcd", "must decode to 32 bytes"},
		{"YOL_HTTP_ADDR", "   ", "is set but empty"},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			env := validAPIEnv()
			env[c.key] = c.value
			setEnv(t, env)

			_, err := LoadAPI()
			if err == nil {
				t.Fatalf("%s=%q accepted", c.key, c.value)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

func TestLoadAgent(t *testing.T) {
	setEnv(t, map[string]string{
		"YOL_CONTROL_PLANE_URL": "https://api.yol.test",
		"YOL_STATE_DIR":         "/etc/yol",
		"YOL_DOCKER_HOST":       "unix:///var/run/docker.sock",
		"YOL_RECONCILE_EVERY":   "10s",
	})

	cfg, err := LoadAgent()
	if err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
	if cfg.ReconcileEvery != 10*time.Second {
		t.Errorf("ReconcileEvery = %s, want 10s", cfg.ReconcileEvery)
	}
}

func TestPort(t *testing.T) {
	got, err := Port(":8080")
	if err != nil || got != 8080 {
		t.Errorf("Port(\":8080\") = %d, %v; want 8080, nil", got, err)
	}
	if _, err := Port("localhost"); err == nil {
		t.Error("Port(\"localhost\") should fail")
	}
}
