package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// EnrollmentWindow is how long a setup token stays usable. Short, because it only has to
// survive the minutes between being written to a server and the agent starting.
const EnrollmentWindow = 30 * time.Minute

const agentTokenBytes = 32

// AgentIdentity is a connected agent's server, resolved from its credential.
type AgentIdentity struct {
	ServerID uuid.UUID
	OrgID    uuid.UUID
	Mode     proto.Mode
}

// NewAgentToken returns a token and the hash to store, so a database leak yields no usable
// agent credential.
func NewAgentToken() (token string, hash []byte, err error) {
	raw := make([]byte, agentTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("server: generate agent token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashAgentToken(token), nil
}

// HashAgentToken derives the stored form of an agent credential.
func HashAgentToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// IssueEnrollmentToken records a single-use token for a server about to be set up, and
// returns the token itself for writing onto the machine.
func (s *Service) IssueEnrollmentToken(ctx context.Context, orgID, serverID uuid.UUID) (string, error) {
	token, hash, err := NewAgentToken()
	if err != nil {
		return "", err
	}

	err = s.pool.InOrg(ctx, orgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).SetServerEnrollmentToken(ctx, sqlc.SetServerEnrollmentTokenParams{
			ID:                  serverID,
			EnrollmentTokenHash: hash,
			EnrollmentExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(EnrollmentWindow), Valid: true},
		})
	})
	if err != nil {
		return "", httpx.Internal(err)
	}
	return token, nil
}

// Enroll trades a setup token for a lasting credential. The token is consumed as it is used,
// so presenting it twice finds nothing.
func (s *Service) Enroll(ctx context.Context, enrollmentToken string) (string, *AgentIdentity, error) {
	if enrollmentToken == "" {
		return "", nil, httpx.NotAuthenticated()
	}

	agentToken, agentHash, err := NewAgentToken()
	if err != nil {
		return "", nil, httpx.Internal(err)
	}

	var identity AgentIdentity
	err = s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		var mode string
		scanErr := tx.QueryRow(ctx,
			`SELECT server_id, server_org_id, server_mode FROM enroll_agent($1, $2)`,
			HashAgentToken(enrollmentToken), agentHash,
		).Scan(&identity.ServerID, &identity.OrgID, &mode)
		if scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				// Expired, already used, or never valid: all the same to the caller.
				return httpx.NotAuthenticated().WithCause(scanErr)
			}
			return httpx.Internal(scanErr)
		}
		identity.Mode = proto.Mode(mode)
		return nil
	})
	if err != nil {
		return "", nil, err
	}

	s.recordAgentEvent(ctx, identity, "agent",
		"The agent on this server has registered itself.", "info")
	return agentToken, &identity, nil
}

// AuthenticateAgent resolves an agent credential to its server. Like enrolling, this happens
// before any organization is in scope.
func (s *Service) AuthenticateAgent(ctx context.Context, token string) (*AgentIdentity, error) {
	if token == "" {
		return nil, httpx.NotAuthenticated()
	}

	var identity AgentIdentity
	err := s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		var mode, status string
		scanErr := tx.QueryRow(ctx,
			`SELECT server_id, server_org_id, server_mode, server_status FROM authenticate_agent($1)`,
			HashAgentToken(token),
		).Scan(&identity.ServerID, &identity.OrgID, &mode, &status)
		if scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return httpx.NotAuthenticated().WithCause(scanErr)
			}
			return httpx.Internal(scanErr)
		}
		identity.Mode = proto.Mode(mode)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

// RecordHeartbeat notes that an agent is alive and refreshes what it reports about the machine.
func (s *Service) RecordHeartbeat(ctx context.Context, identity AgentIdentity, version string, facts proto.HostFacts) error {
	return s.pool.InOrg(ctx, identity.OrgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if err := q.TouchServerAgent(ctx, sqlc.TouchServerAgentParams{
			ID: identity.ServerID, AgentVersion: text(version),
		}); err != nil {
			return err
		}
		// Disks fill and memory changes, so these are refreshed rather than set once.
		return q.UpdateServerFacts(ctx, sqlc.UpdateServerFactsParams{
			ID:            identity.ServerID,
			OsName:        text(facts.OSName),
			OsVersion:     text(facts.OSVersion),
			Arch:          text(facts.Arch),
			Kernel:        text(facts.Kernel),
			CpuCount:      int32Ptr(facts.CPUCount),
			MemoryBytes:   int64Ptr(facts.MemoryBytes),
			DockerVersion: text(facts.DockerVersion),
		})
	})
}

// MarkOffline records that an agent has stopped answering.
func (s *Service) MarkOffline(ctx context.Context, identity AgentIdentity) {
	_ = s.pool.InOrg(ctx, identity.OrgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: identity.ServerID, Status: sqlc.ServerStatusOffline,
		})
	})
}

func (s *Service) recordAgentEvent(ctx context.Context, identity AgentIdentity, step, message, level string) {
	_ = s.pool.InOrg(ctx, identity.OrgID, func(tx pgx.Tx) error {
		return recordEvent(ctx, sqlc.New(tx), identity.OrgID, identity.ServerID, step, message, level)
	})
}
