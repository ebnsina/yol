package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// A container that has started is not a container that is serving. Something has to answer before
// traffic is moved to it, or a deploy is a downtime with extra steps: the old version is taken away
// while the new one is still opening its database connections, or crashing on a bad variable.
//
// The check is made from the machine itself, over the private network the containers share. An app
// publishes nothing, so there is no port on the host to check instead.

const (
	defaultGateTimeout  = 60 * time.Second
	defaultGateInterval = time.Second
	// One attempt is given less than the gap between attempts, so a container that accepts a
	// connection and then stalls does not hold up every remaining try.
	gateAttemptTimeout = 5 * time.Second
)

// errUnhealthy means the container never answered within the time allowed.
var errUnhealthy = errors.New("agent: the container did not start serving in time")

// awaitHealthy waits until the container answers, or gives up. Giving up is a normal outcome: it
// is how a deploy of something broken is caught before it is put in front of anybody.
func (a *Agent) awaitHealthy(ctx context.Context, name string, gate *proto.HealthGate) error {
	timeout := defaultGateTimeout
	if gate.TimeoutSec > 0 {
		timeout = time.Duration(gate.TimeoutSec) * time.Second
	}
	interval := defaultGateInterval
	if gate.IntervalSec > 0 {
		interval = time.Duration(gate.IntervalSec) * time.Second
	}

	gateCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		address, err := a.containerAddress(gateCtx, name)
		if err != nil {
			lastErr = err
		} else if lastErr = probe(gateCtx, address, gate); lastErr == nil {
			return nil
		}

		select {
		case <-gateCtx.Done():
			// A container that stopped on its own has not merely been slow, but there is nothing
			// more useful to say than what the last attempt found.
			return fmt.Errorf("%w: %v", errUnhealthy, lastErr)
		case <-time.After(interval):
		}
	}
}

// containerAddress finds where the container can be reached on the private network.
func (a *Agent) containerAddress(ctx context.Context, name string) (string, error) {
	template := "{{with index .NetworkSettings.Networks \"" + proto.Network + "\"}}{{.IPAddress}}{{end}}"
	address, err := a.docker(ctx, 10*time.Second, "inspect", "-f", template, name)
	if err != nil {
		return "", err
	}
	if address == "" {
		return "", fmt.Errorf("agent: %s is not on the %s network yet", name, proto.Network)
	}
	return address, nil
}

// probe makes one attempt. An HTTP path is asked for when given, since an app that accepts
// connections before it can serve a request is common enough to matter. Redirects are followed and
// the answer at the end is what counts, because a health path often redirects to a sign-in page.
func probe(ctx context.Context, address string, gate *proto.HealthGate) error {
	attemptCtx, cancel := context.WithTimeout(ctx, gateAttemptTimeout)
	defer cancel()

	if gate.HTTPPath != "" {
		port := gate.Port
		if port == 0 {
			port = 80
		}
		url := "http://" + net.JoinHostPort(address, strconv.Itoa(port)) + gate.HTTPPath

		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()

		if res.StatusCode < 200 || res.StatusCode > 299 {
			return fmt.Errorf("agent: %s answered %d", gate.HTTPPath, res.StatusCode)
		}
		return nil
	}

	port := gate.TCPPort
	if port == 0 {
		port = gate.Port
	}
	if port == 0 {
		return errors.New("agent: the health check names neither a path nor a port")
	}

	var dialer net.Dialer
	conn, err := dialer.DialContext(attemptCtx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return conn.Close()
}
