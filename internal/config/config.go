// Package config loads and validates process configuration from the environment.
package config

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// API is the control plane configuration. Every field is required.
type API struct {
	Env             Environment
	HTTPAddr        string
	PublicURL       *url.URL
	WebOrigin       *url.URL
	CookieDomain    string
	DatabaseURL     string
	AgentDir        string
	SessionSecret   []byte
	SecretsKey      []byte
	AgentSpecKey    []byte
	SessionTTL      time.Duration
	ShutdownTimeout time.Duration
}

// Agent is the managed-server binary configuration. Every field is required.
type Agent struct {
	ControlPlaneURL *url.URL
	StateDir        string
	DockerHost      string
	ReconcileEvery  time.Duration
}

// Environment identifies the deployment target and gates environment-specific behaviour.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvStaging     Environment = "staging"
	EnvProduction  Environment = "production"
)

// IsProduction reports whether the process runs against production infrastructure.
func (e Environment) IsProduction() bool { return e == EnvProduction }

// LoadAPI reads the control plane configuration, returning every problem at once.
func LoadAPI() (*API, error) {
	l := newLoader()
	cfg := &API{
		Env:             Environment(l.enum("YOL_ENV", string(EnvDevelopment), string(EnvStaging), string(EnvProduction))),
		HTTPAddr:        l.str("YOL_HTTP_ADDR"),
		PublicURL:       l.url("YOL_PUBLIC_URL"),
		WebOrigin:       l.url("YOL_WEB_ORIGIN"),
		CookieDomain:    l.str("YOL_COOKIE_DOMAIN"),
		DatabaseURL:     l.str("YOL_DATABASE_URL"),
		AgentDir:        l.str("YOL_AGENT_DIR"),
		SessionSecret:   l.key("YOL_SESSION_SECRET", 32),
		SecretsKey:      l.key("YOL_SECRETS_KEY", 32),
		AgentSpecKey:    l.key("YOL_AGENT_SPEC_KEY", 32),
		SessionTTL:      l.duration("YOL_SESSION_TTL"),
		ShutdownTimeout: l.duration("YOL_SHUTDOWN_TIMEOUT"),
	}
	return cfg, l.err()
}

// LoadAgent reads the managed-server configuration, returning every problem at once.
func LoadAgent() (*Agent, error) {
	l := newLoader()
	cfg := &Agent{
		ControlPlaneURL: l.url("YOL_CONTROL_PLANE_URL"),
		StateDir:        l.str("YOL_STATE_DIR"),
		DockerHost:      l.str("YOL_DOCKER_HOST"),
		ReconcileEvery:  l.duration("YOL_RECONCILE_EVERY"),
	}
	return cfg, l.err()
}

// MustLoadAPI loads the control plane configuration or exits naming what is wrong.
func MustLoadAPI() *API {
	cfg, err := LoadAPI()
	if err != nil {
		fail(err)
	}
	return cfg
}

// MustLoadAgent loads the agent configuration or exits naming what is wrong.
func MustLoadAgent() *Agent {
	cfg, err := LoadAgent()
	if err != nil {
		fail(err)
	}
	return cfg
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
	os.Exit(1)
}

// Error reports every invalid or missing variable found in one pass.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return "  - " + strings.Join(e.Problems, "\n  - ")
}

// loader accumulates problems so a misconfigured process reports all of them at once.
type loader struct {
	problems []string
}

func newLoader() *loader { return &loader{} }

func (l *loader) err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return &Error{Problems: l.problems}
}

func (l *loader) reject(key, reason string) {
	l.problems = append(l.problems, fmt.Sprintf("%s %s", key, reason))
}

// raw returns the trimmed value, recording a problem when it is absent or blank.
func (l *loader) raw(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		l.reject(key, "is not set")
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		l.reject(key, "is set but empty")
		return "", false
	}
	return v, true
}

func (l *loader) str(key string) string {
	v, _ := l.raw(key)
	return v
}

func (l *loader) enum(key string, allowed ...string) string {
	v, ok := l.raw(key)
	if !ok {
		return ""
	}
	if slices.Contains(allowed, v) {
		return v
	}
	l.reject(key, fmt.Sprintf("must be one of %s, got %q", strings.Join(allowed, ", "), v))
	return ""
}

func (l *loader) url(key string) *url.URL {
	v, ok := l.raw(key)
	if !ok {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil {
		l.reject(key, fmt.Sprintf("is not a valid URL: %v", err))
		return nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		l.reject(key, fmt.Sprintf("must use http or https, got %q", u.Scheme))
		return nil
	}
	if u.Host == "" {
		l.reject(key, "must include a host")
		return nil
	}
	return u
}

func (l *loader) duration(key string) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.reject(key, fmt.Sprintf("is not a valid duration: %v", err))
		return 0
	}
	if d <= 0 {
		l.reject(key, fmt.Sprintf("must be positive, got %s", d))
		return 0
	}
	return d
}

// key decodes a hex-encoded secret and enforces its exact byte length.
func (l *loader) key(name string, wantBytes int) []byte {
	v, ok := l.raw(name)
	if !ok {
		return nil
	}
	b, err := hex.DecodeString(v)
	if err != nil {
		l.reject(name, "must be hex-encoded: "+err.Error())
		return nil
	}
	if len(b) != wantBytes {
		l.reject(name, fmt.Sprintf("must decode to %d bytes, got %d", wantBytes, len(b)))
		return nil
	}
	return b
}

// Port extracts the numeric port from an address, for logging and health checks.
func Port(addr string) (int, error) {
	_, p, found := strings.Cut(addr, ":")
	if !found {
		return 0, fmt.Errorf("address %q has no port", addr)
	}
	return strconv.Atoi(p)
}
