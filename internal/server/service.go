// Package server owns connecting to a customer's machine and everything we know about it.
package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/ebnsina/yol/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Enqueuer starts the work that outlives the request. An interface so this package does not
// depend on the job library, and so tests can run without one.
type Enqueuer interface {
	EnqueueSurvey(ctx context.Context, tx pgx.Tx, serverID, orgID uuid.UUID) error
}

// Service owns the server rules.
type Service struct {
	pool     *db.Pool
	box      *secrets.Box
	enqueuer Enqueuer
}

// NewService builds the server service.
func NewService(pool *db.Pool, box *secrets.Box, enqueuer Enqueuer) *Service {
	return &Service{pool: pool, box: box, enqueuer: enqueuer}
}

// Mode is how much we may do to a server.
type Mode string

const (
	// ModeManaged means we may install the agent and run containers.
	ModeManaged Mode = "managed"
	// ModeWatch means we look and report, and change nothing.
	ModeWatch Mode = "watch"
)

// Server is a machine as clients see it.
type Server struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Host        string      `json:"host"`
	SSHPort     int         `json:"sshPort"`
	SSHUser     string      `json:"sshUser"`
	Mode        Mode        `json:"mode"`
	Status      Status      `json:"status"`
	RoutingMode *string     `json:"routingMode"`
	Facts       Facts       `json:"facts"`
	Permissions Permissions `json:"permissions"`

	// Present when setup did not finish, in the words shown to the user.
	FailureReason *string    `json:"failureReason"`
	AgentVersion  *string    `json:"agentVersion"`
	AgentLastSeen *time.Time `json:"agentLastSeenAt"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// Status is where a server is in its life.
type Status string

const (
	StatusPending        Status = "pending"
	StatusSurveying      Status = "surveying"
	StatusAwaitingChoice Status = "awaiting_choice"
	StatusInstalling     Status = "installing"
	StatusOnline         Status = "online"
	StatusOffline        Status = "offline"
	StatusFailed         Status = "failed"
)

// Facts describe the machine.
type Facts struct {
	OSName        *string `json:"osName"`
	OSVersion     *string `json:"osVersion"`
	Arch          *string `json:"arch"`
	Kernel        *string `json:"kernel"`
	CPUCount      *int32  `json:"cpuCount"`
	MemoryBytes   *int64  `json:"memoryBytes"`
	DockerVersion *string `json:"dockerVersion"`
}

// Permissions tells the client what the caller may do here, so it renders from this rather
// than working the rules out from a role.
type Permissions struct {
	Manage bool `json:"manage"`
	Delete bool `json:"delete"`
}

// ConnectInput is a request to connect a machine.
type ConnectInput struct {
	Name     string
	Host     string
	Port     int
	User     string
	Mode     Mode
	Key      string
	Password string
}

// Connect records a server and starts looking at it. Nothing is changed on the machine by
// this call or by the survey that follows; the first change waits for the user to see what
// we found and say to continue.
func (s *Service) Connect(ctx context.Context, m *org.Membership, userID uuid.UUID, in ConnectInput) (*Server, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}
	if err := validateConnect(&in); err != nil {
		return nil, err
	}

	sealed, kind, err := s.sealCredential(in)
	if err != nil {
		return nil, err
	}

	serverID := uuid.New()
	var out *Server

	err = s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.CreateServer(ctx, sqlc.CreateServerParams{
			ID:            serverID,
			OrgID:         m.OrgID,
			Name:          in.Name,
			Mode:          sqlc.ServerMode(in.Mode),
			Host:          in.Host,
			SshPort:       int32(in.Port),
			SshUser:       in.User,
			SshSecret:     sealed,
			SshSecretKind: &kind,
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.AlreadyExists("That server is already connected to this organization.").
					WithField("host", "This address is already in use here.")
			}
			return httpx.Internal(err)
		}

		if err := recordEvent(ctx, q, m.OrgID, serverID, "queued",
			"Waiting to look at this server. Nothing has been changed on it.", "info"); err != nil {
			return err
		}
		if err := s.enqueuer.EnqueueSurvey(ctx, tx, serverID, m.OrgID); err != nil {
			return httpx.Internal(err)
		}

		out = toServer(row, m.Role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// List returns the organization's servers.
func (s *Service) List(ctx context.Context, m *org.Membership, userID uuid.UUID) ([]Server, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []Server{}
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListServers(ctx, m.OrgID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, *toServer(row, m.Role))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one server.
func (s *Service) Get(ctx context.Context, m *org.Membership, userID, serverID uuid.UUID) (*Server, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	var out *Server
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		row, err := sqlc.New(tx).GetServer(ctx, serverID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("server").WithCause(err)
			}
			return httpx.Internal(err)
		}
		out = toServer(row, m.Role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete forgets a server. Nothing is removed from the machine itself, because a customer
// disconnecting from us should not lose anything they were running.
func (s *Service) Delete(ctx context.Context, m *org.Membership, userID, serverID uuid.UUID) error {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return err
	}
	return s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		if err := sqlc.New(tx).DeleteServer(ctx, sqlc.DeleteServerParams{ID: serverID, OrgID: m.OrgID}); err != nil {
			return httpx.Internal(err)
		}
		return nil
	})
}

// Event is a step in a server's setup.
type Event struct {
	Step      string    `json:"step"`
	Message   string    `json:"message"`
	Level     string    `json:"level"`
	CreatedAt time.Time `json:"createdAt"`
}

// Events returns progress since a moment, so a client can follow along without refetching
// everything each time.
func (s *Service) Events(ctx context.Context, m *org.Membership, userID, serverID uuid.UUID, since time.Time) ([]Event, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []Event{}
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListServerEvents(ctx, sqlc.ListServerEventsParams{
			ServerID:  serverID,
			CreatedAt: toTimestamp(since),
		})
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, Event{
				Step:      row.Step,
				Message:   row.Message,
				Level:     row.Level,
				CreatedAt: row.CreatedAt.Time,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Resource is something found on a server, ours or theirs.
type Resource struct {
	ID         uuid.UUID  `json:"id"`
	Kind       string     `json:"kind"`
	ExternalID string     `json:"externalId"`
	Name       string     `json:"name"`
	Status     *string    `json:"status"`
	Image      *string    `json:"image"`
	Version    *string    `json:"version"`
	Ports      []int32    `json:"ports"`
	SizeBytes  *int64     `json:"sizeBytes"`
	Managed    bool       `json:"managed"`
	AdoptedAt  *time.Time `json:"adoptedAt"`
	Details    any        `json:"details,omitempty"`
	LastSeenAt time.Time  `json:"lastSeenAt"`
}

// Resources returns everything found on a server. Unmanaged resources are included on
// purpose: a machine that is already in use should be shown as it is.
func (s *Service) Resources(ctx context.Context, m *org.Membership, userID, serverID uuid.UUID) ([]Resource, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []Resource{}
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListDiscoveredResources(ctx, serverID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, Resource{
				ID:         row.ID,
				Kind:       string(row.Kind),
				ExternalID: row.ExternalID,
				Name:       row.Name,
				Status:     row.Status,
				Image:      row.Image,
				Version:    row.Version,
				Ports:      row.Ports,
				SizeBytes:  row.SizeBytes,
				Managed:    row.Managed,
				AdoptedAt:  timeOrNil(row.AdoptedAt),
				LastSeenAt: row.LastSeenAt.Time,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sealCredential encrypts the key or password before it goes anywhere near the database.
func (s *Service) sealCredential(in ConnectInput) ([]byte, string, error) {
	switch {
	case in.Key != "":
		return s.box.SealString(in.Key, secrets.ContextSSHCredential), "key", nil
	case in.Password != "":
		return s.box.SealString(in.Password, secrets.ContextSSHCredential), "password", nil
	default:
		return nil, "", httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("key", "Add a private key, or a password instead.")
	}
}

func validateConnect(in *ConnectInput) *httpx.Error {
	in.Name = strings.TrimSpace(in.Name)
	in.Host = strings.TrimSpace(in.Host)
	in.User = strings.TrimSpace(in.User)

	if in.User == "" {
		in.User = "root"
	}
	if in.Port == 0 {
		in.Port = 22
	}
	if in.Mode == "" {
		in.Mode = ModeManaged
	}

	if in.Name == "" {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("name", "Give this server a name you will recognise.")
	}
	if in.Host == "" {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("host", "Enter the address of your server.")
	}
	// Only obvious mistakes are caught here. Whether the address actually works is settled by
	// trying it, and that failure is reported far more usefully than a guess at the format.
	if strings.ContainsAny(in.Host, " \t") {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("host", "Enter just the address, with no spaces.")
	}
	if strings.Contains(in.Host, "@") {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("host", "Enter only the address here. The username goes in its own field.")
	}
	if strings.Contains(in.Host, "/") {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("host", "Enter just the address, without a web address around it.")
	}
	if in.Port < 1 || in.Port > 65535 {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("sshPort", "Enter a port between 1 and 65535.")
	}
	if in.Mode != ModeManaged && in.Mode != ModeWatch {
		return httpx.InvalidInput("Please choose how this server should be used.").
			WithField("mode", "Choose whether we manage this server or only watch it.")
	}
	return nil
}

func toServer(row sqlc.Server, role org.Role) *Server {
	var routing *string
	if row.RoutingMode != nil {
		value := string(*row.RoutingMode)
		routing = &value
	}
	return &Server{
		ID:          row.ID,
		Name:        row.Name,
		Host:        row.Host,
		SSHPort:     int(row.SshPort),
		SSHUser:     row.SshUser,
		Mode:        Mode(row.Mode),
		Status:      Status(row.Status),
		RoutingMode: routing,
		Facts: Facts{
			OSName:        row.OsName,
			OSVersion:     row.OsVersion,
			Arch:          row.Arch,
			Kernel:        row.Kernel,
			CPUCount:      row.CpuCount,
			MemoryBytes:   row.MemoryBytes,
			DockerVersion: row.DockerVersion,
		},
		Permissions: Permissions{
			Manage: role.Can(org.CanManageServers),
			Delete: role.Can(org.CanManageServers),
		},
		FailureReason: row.FailureReason,
		AgentVersion:  row.AgentVersion,
		AgentLastSeen: timeOrNil(row.AgentLastSeenAt),
		CreatedAt:     row.CreatedAt.Time,
	}
}

// ModeOf converts to the value the agent understands.
func ModeOf(mode Mode) proto.Mode {
	if mode == ModeWatch {
		return proto.ModeWatch
	}
	return proto.ModeManaged
}
