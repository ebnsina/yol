// Package ssh connects to customer servers. It is used to look at a machine and to install
// the agent, and for nothing afterwards: once the agent is running, it holds the connection.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	dialTimeout    = 15 * time.Second
	commandTimeout = 60 * time.Second
)

// Credential is how to authenticate. Exactly one of Key or Password is used.
type Credential struct {
	User       string
	Key        string
	Passphrase string
	Password   string
}

// Target is a machine to connect to.
type Target struct {
	Host string
	Port int
}

// Addr renders the dial address.
func (t Target) Addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

// Client is an open connection to one server.
type Client struct {
	conn *ssh.Client
}

// Reasons a connection failed, kept separate so the interface can say something useful
// instead of showing a transport error.
var (
	ErrUnreachable  = errors.New("ssh: server did not answer")
	ErrAuthRejected = errors.New("ssh: server rejected the credentials")
	ErrBadKey       = errors.New("ssh: the key could not be read")
)

// Dial opens a connection.
//
// Host keys are accepted on first sight and recorded by the caller. Verifying them properly
// needs a key the user has no way to give us before the first connection, and refusing to
// connect without one would make the product unusable. The exposure is a first-connection
// interception, which is the same trade-off ssh itself makes by default.
func Dial(ctx context.Context, target Target, cred Credential) (*Client, string, error) {
	auth, err := authMethods(cred)
	if err != nil {
		return nil, "", err
	}

	var hostKey string
	cfg := &ssh.ClientConfig{
		User:    cred.User,
		Auth:    auth,
		Timeout: dialTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostKey = string(ssh.MarshalAuthorizedKey(key))
			return nil
		},
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", target.Addr())
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnreachable, err)
	}

	// The handshake has no context, so the deadline stands in for cancellation.
	if deadline, ok := ctx.Deadline(); ok {
		_ = rawConn.SetDeadline(deadline)
	} else {
		_ = rawConn.SetDeadline(time.Now().Add(dialTimeout))
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, target.Addr(), cfg)
	if err != nil {
		rawConn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, "", fmt.Errorf("%w: %v", ErrAuthRejected, err)
		}
		return nil, "", fmt.Errorf("ssh: handshake failed: %w", err)
	}
	_ = rawConn.SetDeadline(time.Time{})

	return &Client{conn: ssh.NewClient(sshConn, chans, reqs)}, strings.TrimSpace(hostKey), nil
}

func authMethods(cred Credential) ([]ssh.AuthMethod, error) {
	switch {
	case cred.Key != "":
		var signer ssh.Signer
		var err error
		if cred.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cred.Key), []byte(cred.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cred.Key))
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadKey, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil

	case cred.Password != "":
		return []ssh.AuthMethod{ssh.Password(cred.Password)}, nil

	default:
		return nil, errors.New("ssh: no key or password supplied")
	}
}

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Result is the outcome of one command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Ok reports whether the command succeeded.
func (r Result) Ok() bool { return r.ExitCode == 0 }

// Run executes a command and collects its output. A non-zero exit is returned in Result
// rather than as an error, because probing a machine means expecting commands to be missing.
func (c *Client) Run(ctx context.Context, command string) (Result, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("ssh: open session: %w", err)
	}
	defer session.Close()

	var stdout, stderr strings.Builder
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	timeout := time.NewTimer(commandTimeout)
	defer timeout.Stop()

	select {
	case err := <-done:
		result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if err != nil {
			if exitErr, ok := errors.AsType[*ssh.ExitError](err); ok {
				result.ExitCode = exitErr.ExitStatus()
				return result, nil
			}
			return result, fmt.Errorf("ssh: run %q: %w", firstWord(command), err)
		}
		return result, nil

	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return Result{}, ctx.Err()

	case <-timeout.C:
		_ = session.Signal(ssh.SIGKILL)
		return Result{}, fmt.Errorf("ssh: %q did not finish in %s", firstWord(command), commandTimeout)
	}
}

// Output runs a command and returns trimmed stdout, empty when it failed. Used for probes
// where a missing tool is an ordinary answer.
func (c *Client) Output(ctx context.Context, command string) string {
	result, err := c.Run(ctx, command)
	if err != nil || !result.Ok() {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// firstWord keeps error messages readable without echoing a whole shell pipeline.
func firstWord(command string) string {
	if before, _, found := strings.Cut(command, " "); found {
		return before
	}
	return command
}
