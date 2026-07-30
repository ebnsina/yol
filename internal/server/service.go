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
	EnqueueBootstrap(ctx context.Context, tx pgx.Tx, serverID, orgID uuid.UUID) error
}

// Service owns the server rules.
type Service struct {
	pool     *db.Pool
	box      *secrets.Box
	enqueuer Enqueuer
}

// NewService builds the server service.
func NewService(pool *db.Pool, box *secrets.Box) *Service {
	return &Service{pool: pool, box: box}
}

// SetEnqueuer supplies the queue once it exists. The queue's workers are built from this
// service, so one of the two has to be completed after the other.
func (s *Service) SetEnqueuer(enqueuer Enqueuer) {
	s.enqueuer = enqueuer
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

// Routing is how web traffic reaches apps on a server.
type Routing string

const (
	// RoutingTakeover means we handle ports 80 and 443 ourselves, with certificates.
	RoutingTakeover Routing = "takeover"
	// RoutingBehindProxy means their web server keeps those ports and we serve on others.
	RoutingBehindProxy Routing = "behind_proxy"
)

// ChooseRouting answers the question the survey asked. Recorded before anything is
// installed, because the answer decides whether their web server keeps its ports.
func (s *Service) ChooseRouting(ctx context.Context, m *org.Membership, userID, serverID uuid.UUID, choice Routing) (*Server, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}
	if choice != RoutingTakeover && choice != RoutingBehindProxy {
		return nil, httpx.InvalidInput("Please choose how web traffic should reach your apps.").
			WithField("routingMode", "Choose one of the available options.")
	}

	var out *Server
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetServer(ctx, serverID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("server").WithCause(err)
			}
			return httpx.Internal(err)
		}
		if row.Mode == sqlc.ServerModeWatch {
			return httpx.Conflict("This server is being watched only, so nothing is being set up on it.")
		}

		mode := sqlc.RoutingMode(choice)
		if err := q.SetServerRoutingMode(ctx, sqlc.SetServerRoutingModeParams{
			ID: serverID, RoutingMode: &mode,
		}); err != nil {
			return httpx.Internal(err)
		}

		message := "Your apps will be served on ports we choose, so your existing web server keeps ports 80 and 443."
		if choice == RoutingTakeover {
			message = "We will handle ports 80 and 443 on this server, including certificates."
		}
		if err := recordEvent(ctx, q, m.OrgID, serverID, "routing", message, "info"); err != nil {
			return err
		}

		row.RoutingMode = &mode
		out = toServer(row, m.Role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Setup begins installing on a server. This is the first thing that changes a customer's
// machine, and it happens only when they ask for it after seeing what we found.
func (s *Service) Setup(ctx context.Context, m *org.Membership, userID, serverID uuid.UUID) (*Server, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}

	var out *Server
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetServer(ctx, serverID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("server").WithCause(err)
			}
			return httpx.Internal(err)
		}

		if row.Mode == sqlc.ServerModeWatch {
			return httpx.Conflict(
				"This server is being watched only. Nothing is installed on a watched server.")
		}
		if row.Status == sqlc.ServerStatusInstalling {
			return httpx.Conflict("This server is already being set up.")
		}
		if row.Status == sqlc.ServerStatusOnline {
			return httpx.Conflict("This server is already set up and connected.")
		}
		if row.RoutingMode == nil {
			return httpx.Conflict("Choose how web traffic should reach your apps first.")
		}

		if err := recordEvent(ctx, q, m.OrgID, serverID, "install",
			"Setting up this server.", "info"); err != nil {
			return err
		}
		if err := q.UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: serverID, Status: sqlc.ServerStatusInstalling,
		}); err != nil {
			return httpx.Internal(err)
		}
		if err := s.enqueuer.EnqueueBootstrap(ctx, tx, serverID, m.OrgID); err != nil {
			return httpx.Internal(err)
		}

		row.Status = sqlc.ServerStatusInstalling
		out = toServer(row, m.Role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Event is a step in a server's setup. It carries its own identifier because several are
// often written in one transaction and therefore share a timestamp exactly.
type Event struct {
	ID        uuid.UUID `json:"id"`
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
				ID:        row.ID,
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
