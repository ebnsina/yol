package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/ebnsina/yol/internal/secrets"
	"github.com/ebnsina/yol/internal/ssh"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SurveyArgs identifies which server to look at. Identifiers only: job arguments are stored
// as readable JSON and kept after the job ends, so credentials are loaded by the worker
// instead of travelling here.
type SurveyArgs struct {
	ServerID uuid.UUID `json:"serverId"`
	OrgID    uuid.UUID `json:"orgId"`
}

// Kind names the job.
func (SurveyArgs) Kind() string { return "server.survey" }

// Surveyor looks at a server and records what is on it. It changes nothing on the machine.
type Surveyor struct {
	pool *db.Pool
	box  *secrets.Box
}

// NewSurveyor builds the survey worker.
func NewSurveyor(pool *db.Pool, box *secrets.Box) *Surveyor {
	return &Surveyor{pool: pool, box: box}
}

// Run performs the survey. Every failure is recorded against the server in words the person
// waiting can act on, because a five-minute wait ending in silence is the worst outcome.
func (s *Surveyor) Run(ctx context.Context, args SurveyArgs) error {
	target, cred, err := s.loadTarget(ctx, args)
	if err != nil {
		s.fail(ctx, args, "connect", err)
		return nil // recorded against the server; retrying would not help
	}

	s.setStatus(ctx, args, StatusSurveying, nil)
	s.event(ctx, args, "connect", fmt.Sprintf("Connecting to %s.", target.Host), "info")

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, _, err := ssh.Dial(dialCtx, target, cred)
	if err != nil {
		s.fail(ctx, args, "connect", err)
		return nil
	}
	defer client.Close()

	s.event(ctx, args, "survey",
		"Connected. Looking at what is already on this server; nothing is being changed.", "info")

	surveyCtx, cancelSurvey := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelSurvey()

	result, err := ssh.Survey(surveyCtx, client)
	if err != nil {
		s.fail(ctx, args, "survey", err)
		return nil
	}

	if err := s.record(ctx, args, result); err != nil {
		s.fail(ctx, args, "survey", err)
		return nil
	}
	return nil
}

// loadTarget reads the connection details and decrypts the credential.
func (s *Surveyor) loadTarget(ctx context.Context, args SurveyArgs) (ssh.Target, ssh.Credential, error) {
	var target ssh.Target
	var cred ssh.Credential

	err := s.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		row, err := sqlc.New(tx).GetServer(ctx, args.ServerID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("this server is no longer connected")
			}
			return err
		}

		target = ssh.Target{Host: row.Host, Port: int(row.SshPort)}
		cred.User = row.SshUser

		if row.SshSecret == nil || row.SshSecretKind == nil {
			return errors.New("no credentials are stored for this server")
		}
		plain, err := s.box.OpenString(row.SshSecret, secrets.ContextSSHCredential)
		if err != nil {
			return errors.New("the stored credentials for this server could not be read")
		}
		if *row.SshSecretKind == "password" {
			cred.Password = plain
		} else {
			cred.Key = plain
		}
		return nil
	})
	return target, cred, err
}

// record stores the facts, everything found, and what to do next.
func (s *Surveyor) record(ctx context.Context, args SurveyArgs, result *proto.SurveyResult) error {
	return s.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)

		if err := q.UpdateServerFacts(ctx, sqlc.UpdateServerFactsParams{
			ID:            args.ServerID,
			OsName:        text(result.Facts.OSName),
			OsVersion:     text(result.Facts.OSVersion),
			Arch:          text(result.Facts.Arch),
			Kernel:        text(result.Facts.Kernel),
			CpuCount:      int32Ptr(result.Facts.CPUCount),
			MemoryBytes:   int64Ptr(result.Facts.MemoryBytes),
			DockerVersion: text(result.Facts.DockerVersion),
		}); err != nil {
			return err
		}

		if err := storeInventory(ctx, q, args, result.Inventory); err != nil {
			return err
		}
		// Anything not seen this time has gone from the machine.
		if _, err := q.DeleteStaleDiscoveredResources(ctx, args.ServerID); err != nil {
			return err
		}

		return s.reportFindings(ctx, q, args, result)
	})
}

// reportFindings writes the summary the user reads, and decides what has to happen next.
func (s *Surveyor) reportFindings(ctx context.Context, q *sqlc.Queries, args SurveyArgs, result *proto.SurveyResult) error {
	inv := result.Inventory

	summary := fmt.Sprintf("This server runs %s %s with %d processors and %s of memory.",
		orText(result.Facts.OSName, "an unrecognised system"), result.Facts.OSVersion,
		result.Facts.CPUCount, humanBytes(result.Facts.MemoryBytes))
	if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "survey", summary, "info"); err != nil {
		return err
	}

	if existing := countUnmanaged(inv.Containers); existing > 0 {
		message := fmt.Sprintf(
			"Found %s already on this server, plus %d service%s. None of it will be changed.",
			plural(existing, "container"), len(inv.Services), plural2(len(inv.Services)))
		if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "survey", message, "info"); err != nil {
			return err
		}
	}

	for _, database := range inv.Databases {
		message := fmt.Sprintf("Found what looks like %s on port %d, from %s.",
			string(database.Engine), database.Port, database.Source)
		if database.Confidence != proto.ConfidenceCertain {
			message += " We are not certain about this one."
		}
		if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "survey", message, "info"); err != nil {
			return err
		}
	}

	for _, note := range inv.Incomplete {
		if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "survey", note, "warning"); err != nil {
			return err
		}
	}

	if result.Unsupported != "" {
		if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "survey", result.Unsupported, "error"); err != nil {
			return err
		}
		return q.UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: args.ServerID, Status: sqlc.ServerStatusFailed, FailureReason: &result.Unsupported,
		})
	}

	// A watched server is finished here: there is nothing further to install.
	if isWatchOnly(ctx, q, args.ServerID) {
		if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "done",
			"Finished. This server is being watched only, so nothing on it has been changed.", "info"); err != nil {
			return err
		}
		return q.UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: args.ServerID, Status: sqlc.ServerStatusPending,
		})
	}

	// A conflict on the router's ports is the user's decision, never ours.
	if len(result.Conflicts) > 0 {
		for _, conflict := range result.Conflicts {
			if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "conflict",
				describeConflict(conflict), "warning"); err != nil {
				return err
			}
		}
		if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "conflict",
			"Choose how web traffic should reach your apps before we continue.", "warning"); err != nil {
			return err
		}
		return q.UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: args.ServerID, Status: sqlc.ServerStatusAwaitingChoice,
		})
	}

	if err := recordEvent(ctx, q, args.OrgID, args.ServerID, "ready",
		"Ports 80 and 443 are free, so we can handle web traffic here. Ready to set up.", "info"); err != nil {
		return err
	}
	if err := q.SetServerRoutingMode(ctx, sqlc.SetServerRoutingModeParams{
		ID: args.ServerID, RoutingMode: routing(sqlc.RoutingModeTakeover),
	}); err != nil {
		return err
	}
	return q.UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
		ID: args.ServerID, Status: sqlc.ServerStatusAwaitingChoice,
	})
}

// RecordInventory stores what a connected agent reports. Same path as the survey, so a
// machine looks the same however we learned about it.
func (s *Service) RecordInventory(ctx context.Context, identity AgentIdentity, inv proto.Inventory) error {
	args := SurveyArgs{ServerID: identity.ServerID, OrgID: identity.OrgID}

	return s.pool.InOrg(ctx, identity.OrgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if err := storeInventory(ctx, q, args, inv); err != nil {
			return err
		}
		_, err := q.DeleteStaleDiscoveredResources(ctx, identity.ServerID)
		return err
	})
}

// storeInventory writes every resource found, managed or not.
func storeInventory(ctx context.Context, q *sqlc.Queries, args SurveyArgs, inv proto.Inventory) error {
	record := func(kind sqlc.DiscoveredKind, externalID, name string, params sqlc.RecordDiscoveredResourceParams) error {
		params.ID = uuid.New()
		params.OrgID = args.OrgID
		params.ServerID = args.ServerID
		params.Kind = kind
		params.ExternalID = externalID
		params.Name = name
		if params.Details == nil {
			params.Details = []byte(`{}`)
		}
		// An explicit null would be rejected by the column, and the default does not apply
		// when a value is given, so an absent list has to be an empty one.
		if params.Ports == nil {
			params.Ports = []int32{}
		}
		return q.RecordDiscoveredResource(ctx, params)
	}

	for _, container := range inv.Containers {
		details, _ := json.Marshal(map[string]any{
			"state":          container.State,
			"composeProject": container.ComposeProject,
			"createdAt":      container.CreatedAt,
		})
		if err := record(sqlc.DiscoveredKindContainer, container.Name, container.Name,
			sqlc.RecordDiscoveredResourceParams{
				Status:  text(container.Status),
				Image:   text(container.Image),
				Ports:   int32s(container.Ports),
				Managed: container.Managed,
				Details: details,
			}); err != nil {
			return err
		}
	}

	for _, volume := range inv.Volumes {
		if err := record(sqlc.DiscoveredKindVolume, volume.Name, volume.Name,
			sqlc.RecordDiscoveredResourceParams{
				Managed:   volume.Managed,
				SizeBytes: int64Ptr(volume.SizeBytes),
			}); err != nil {
			return err
		}
	}

	for _, service := range inv.Services {
		if err := record(sqlc.DiscoveredKindService, service.Name, service.Name,
			sqlc.RecordDiscoveredResourceParams{Status: text(service.Active)}); err != nil {
			return err
		}
	}

	for _, port := range inv.Ports {
		details, _ := json.Marshal(map[string]any{"process": port.Process, "address": port.Address})
		if err := record(sqlc.DiscoveredKindPort, fmt.Sprintf("%s/%d", port.Protocol, port.Number),
			fmt.Sprintf("%d", port.Number),
			sqlc.RecordDiscoveredResourceParams{
				Ports:   []int32{int32(port.Number)},
				Details: details,
			}); err != nil {
			return err
		}
	}

	for _, database := range inv.Databases {
		details, _ := json.Marshal(map[string]any{
			"confidence": database.Confidence,
			"source":     database.Source,
			"inDocker":   database.InDocker,
		})
		if err := record(sqlc.DiscoveredKindDatabase, string(database.Engine)+":"+database.Source,
			string(database.Engine),
			sqlc.RecordDiscoveredResourceParams{
				Version: text(database.Version),
				Ports:   []int32{int32(database.Port)},
				Managed: database.Managed,
				Details: details,
			}); err != nil {
			return err
		}
	}
	return nil
}

func describeConflict(conflict proto.RoutingConflict) string {
	switch {
	case conflict.Container != "":
		return fmt.Sprintf("Port %d is in use by a container called %s.", conflict.Port, conflict.Container)
	case conflict.Process != "":
		return fmt.Sprintf("Port %d is in use by %s.", conflict.Port, conflict.Process)
	default:
		return fmt.Sprintf("Port %d is already in use.", conflict.Port)
	}
}

func isWatchOnly(ctx context.Context, q *sqlc.Queries, serverID uuid.UUID) bool {
	row, err := q.GetServer(ctx, serverID)
	return err == nil && row.Mode == sqlc.ServerModeWatch
}

// fail records a failure in words the person waiting can act on, and logs the technical
// cause separately so a support question can actually be answered.
func (s *Surveyor) fail(ctx context.Context, args SurveyArgs, step string, cause error) {
	slog.Error("server survey failed",
		"serverId", args.ServerID,
		"orgId", args.OrgID,
		"step", step,
		"error", cause,
	)
	message := explain(cause)
	s.event(ctx, args, step, message, "error")
	s.setStatus(ctx, args, StatusFailed, &message)
}

// explain turns a technical failure into something a user can do something about.
func explain(err error) string {
	switch {
	case errors.Is(err, ssh.ErrUnreachable):
		return "We could not reach this server. Check the address and that it allows connections from the internet."
	case errors.Is(err, ssh.ErrAuthRejected):
		return "The server refused those credentials. Check the username and that the key or password is correct."
	case errors.Is(err, ssh.ErrBadKey):
		return "That private key could not be read. Paste the whole key, including its first and last lines."
	case errors.Is(err, context.DeadlineExceeded):
		return "The server took too long to answer. It may be under heavy load."
	default:
		return "We could not finish looking at this server. Please try again."
	}
}

func (s *Surveyor) event(ctx context.Context, args SurveyArgs, step, message, level string) {
	_ = s.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		return recordEvent(ctx, sqlc.New(tx), args.OrgID, args.ServerID, step, message, level)
	})
}

func (s *Surveyor) setStatus(ctx context.Context, args SurveyArgs, status Status, reason *string) {
	_ = s.pool.InOrg(ctx, args.OrgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).UpdateServerStatus(ctx, sqlc.UpdateServerStatusParams{
			ID: args.ServerID, Status: sqlc.ServerStatus(status), FailureReason: reason,
		})
	})
}
