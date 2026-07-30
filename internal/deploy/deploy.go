package deploy

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/github"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A deploy is recorded here and carried out on the customer's own server. The control plane's part
// is to write down what should be running and to hand the server what it needs: where the code is,
// a credential that lasts an hour, and what the app runs with. Everything after that happens there.

// DeployTarget is one thing a deploy acts on: an app in one environment, on one server, built from
// one repository. Assembled by whoever asked for the deploy, whether that was a push arriving or
// somebody pressing a button.
type DeployTarget struct {
	OrgID         uuid.UUID
	ProjectID     uuid.UUID
	EnvironmentID uuid.UUID
	ServiceID     uuid.UUID
	// Absent when the environment has not been given a server yet, which is the one thing that
	// makes a project undeployable rather than merely unconfigured.
	ServerID *uuid.UUID
	// Where the code is, and the access that reads it. Absent until a repository is connected.
	RepoFullName   *string
	InstallationID *string
}

// Agents is how a deploy reaches the machine it is deploying to. An interface so this package needs
// no knowledge of connections or signing, and so a deploy can be exercised without either.
type Agents interface {
	// Build asks a server to turn a commit into an image.
	Build(ctx context.Context, serverID uuid.UUID, req proto.BuildRequest) error
	// Reconcile hands a server its desired state again, which is what starts the rollout once an
	// image exists.
	Reconcile(ctx context.Context, serverID uuid.UUID) error
}

// SetAgents gives the service a way to reach servers.
func (p *Projects) SetAgents(agents Agents) { p.agents = agents }

// SetWebhookSecret gives the service the secret GitHub signs deliveries with.
func (p *Projects) SetWebhookSecret(secret string) { p.webhookSecret = secret }

// How much of a server a build may take. A build shares the machine with the site it is being
// deployed for, so it is capped rather than left to take what it likes.
const (
	buildMemoryBytes = 1 << 30
	buildCPUPercent  = 50
)

// Deploy records a deployment of one service at one commit and sets it going.
//
// Everything is written down before the server is asked to do anything, so a deploy that is
// interrupted halfway is a deployment sitting in a readable state rather than a build happening
// with nothing to attach it to.
func (p *Projects) Deploy(ctx context.Context, target DeployTarget, commitSHA, ref string) (uuid.UUID, error) {
	if p.agents == nil || p.code == nil {
		return uuid.Nil, httpx.Internal(errNoCode)
	}
	if target.ServerID == nil {
		return uuid.Nil, httpx.InvalidInput("This environment has no server to deploy to yet.").
			WithField("serverId", "Choose a server first.")
	}
	if commitSHA == "" || target.RepoFullName == nil || target.InstallationID == nil {
		return uuid.Nil, httpx.InvalidInput("This project has no connected repository to deploy from.")
	}

	installationID, err := strconv.ParseInt(*target.InstallationID, 10, 64)
	if err != nil {
		return uuid.Nil, httpx.Internal(err)
	}

	// Minted per deploy and lasting an hour. Never stored: it goes straight to the server that
	// needs it, and if a build outlives it the next attempt gets a new one.
	token, err := p.code.InstallationToken(ctx, installationID)
	if err != nil {
		return uuid.Nil, httpx.Internal(err)
	}

	deploymentID := uuid.New()
	var request proto.BuildRequest

	err = p.pool.InOrg(ctx, target.OrgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)

		if _, err := q.CreateDeployment(ctx, sqlc.CreateDeploymentParams{
			ID:        deploymentID,
			OrgID:     target.OrgID,
			ServiceID: target.ServiceID,
			CommitRef: &ref,
			CommitSha: &commitSHA,
		}); err != nil {
			return httpx.Internal(err)
		}
		if err := q.SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
			ID:     deploymentID,
			Status: sqlc.DeploymentStatusBuilding,
		}); err != nil {
			return httpx.Internal(err)
		}

		// Where this version will run. Written now so the desired state already names it by the
		// time the image exists, and named for the deployment so it starts alongside whatever is
		// serving rather than in place of it.
		if _, err := q.CreatePlacement(ctx, sqlc.CreatePlacementParams{
			ID:            uuid.New(),
			OrgID:         target.OrgID,
			DeploymentID:  deploymentID,
			ServerID:      *target.ServerID,
			ContainerName: ContainerNameFor(target.ServiceID, deploymentID),
		}); err != nil {
			return httpx.Internal(err)
		}

		variables, err := p.variablesFor(ctx, q, target.EnvironmentID)
		if err != nil {
			return err
		}

		request = proto.BuildRequest{
			DeploymentID: deploymentID.String(),
			ServiceID:    target.ServiceID.String(),
			SourceURL:    p.code.SourceURL(*target.RepoFullName, commitSHA),
			CommitSHA:    commitSHA,
			SourceToken:  token,
			ImageRef:     ImageRefFor(target.ServiceID, commitSHA),
			Labels: proto.ManagedLabels(target.OrgID.String(), target.ProjectID.String(),
				target.EnvironmentID.String(), target.ServiceID.String(), deploymentID.String(),
				proto.RoleApp),
			// What a build needs and what an app runs with are usually the same, and a build that
			// cannot see them fails in ways nobody expects.
			BuildEnv:         variables,
			MemoryLimitBytes: buildMemoryBytes,
			CPUPercent:       buildCPUPercent,
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}

	if err := p.agents.Build(ctx, *target.ServerID, request); err != nil {
		// The server could not be reached, so the deployment is marked failed now rather than
		// waiting for a report that will never arrive.
		p.failDeployment(ctx, target.OrgID, deploymentID,
			"This server is not connected, so nothing could be built on it.")
		return uuid.Nil, httpx.Internal(err)
	}
	return deploymentID, nil
}

// failDeployment records a deploy that could not be started, so it does not sit as though it were
// still in progress.
func (p *Projects) failDeployment(ctx context.Context, orgID, deploymentID uuid.UUID, reason string) {
	err := p.pool.InOrg(ctx, orgID, func(tx pgx.Tx) error {
		return sqlc.New(tx).SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
			ID:            deploymentID,
			Status:        sqlc.DeploymentStatusFailed,
			FailureReason: &reason,
		})
	})
	if err != nil {
		slog.Error("could not record a deploy that failed to start",
			"deployment", deploymentID, "error", err)
	}
}

// ImageRefFor names the image a deployment produces. Derived from the service and the commit rather
// than stored, so the control plane and the server cannot disagree about it, and so an image already
// on the machine can be recognised when rolling back to it.
func ImageRefFor(serviceID uuid.UUID, commitSHA string) string {
	short := commitSHA
	if len(short) > 12 {
		short = short[:12]
	}
	return "yol/" + serviceID.String()[:12] + ":" + short
}

// ContainerNameFor is how a service's container is named on a machine. The deployment is part of the
// name, so a new version runs alongside the one serving rather than replacing it.
func ContainerNameFor(serviceID, deploymentID uuid.UUID) string {
	return "yol-" + serviceID.String()[:12] + "-" + deploymentID.String()[:8]
}

// VerifyDelivery checks that a delivery really came from GitHub.
func (p *Projects) VerifyDelivery(body []byte, signature string) error {
	return github.Verify(p.webhookSecret, body, signature)
}

// Deliver acts on a delivery that has already been verified.
//
// Nothing here is returned to GitHub: a delivery is answered before this runs, because a build takes
// minutes and GitHub waits seconds. Failures are recorded where the person who pushed will see them.
func (p *Projects) Deliver(ctx context.Context, event string, body []byte) {
	switch event {
	case "push":
		p.deliverPush(ctx, body)

	case "installation", "installation_repositories":
		p.deliverInstallation(ctx, body)

	default:
		// Everything else GitHub sends is of no interest, and refusing it would only make the
		// delivery look broken on their side.
	}
}

func (p *Projects) deliverPush(ctx context.Context, body []byte) {
	push, err := github.ParsePush(body)
	if err != nil {
		slog.Warn("could not read a push from GitHub", "error", err)
		return
	}
	// Something that is not a branch, or a branch being deleted, is a push with nothing to build.
	if push == nil || push.Deleted || push.CommitSHA == "" {
		return
	}

	targets, err := p.deployTargets(ctx, strconv.FormatInt(push.RepositoryID, 10), push.Branch)
	if err != nil {
		slog.Error("could not work out what a push should deploy",
			"repository", push.FullName, "branch", push.Branch, "error", err)
		return
	}
	if len(targets) == 0 {
		// Normal: a push to a branch no environment follows, or to a repository nobody connected.
		return
	}

	for _, target := range targets {
		// Detached from the delivery, which has already been answered.
		go func(target DeployTarget) {
			deployCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()

			if _, err := p.Deploy(deployCtx, target, push.CommitSHA, push.Branch); err != nil {
				slog.Error("a push could not be deployed",
					"repository", push.FullName, "branch", push.Branch, "error", err)
			}
		}(target)
	}
}

// deployTargets works out which environments a push should deploy. Answered before any organization
// is in scope, since a delivery names only a repository and a branch.
func (p *Projects) deployTargets(ctx context.Context, repositoryID, branch string) ([]DeployTarget, error) {
	var targets []DeployTarget

	err := p.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		// Written out rather than generated, because a function that returns rows is something the
		// query generator cannot type. The same is true of every other lookup that has to answer
		// before an organization is in scope.
		rows, err := tx.Query(ctx,
			`SELECT org_id, project_id, environment_id, service_id, server_id,
			        repo_full_name, installation_id
			 FROM find_deploy_targets($1, $2)`, repositoryID, branch)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var target DeployTarget
			if err := rows.Scan(&target.OrgID, &target.ProjectID, &target.EnvironmentID,
				&target.ServiceID, &target.ServerID, &target.RepoFullName,
				&target.InstallationID); err != nil {
				return err
			}
			targets = append(targets, target)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return targets, nil
}

// deliverInstallation records access being taken away, so a project can say why it stopped
// deploying rather than failing on the next push with nothing to explain it.
func (p *Projects) deliverInstallation(ctx context.Context, body []byte) {
	event, err := github.ParseInstallation(body)
	if err != nil {
		slog.Warn("could not read an installation change from GitHub", "error", err)
		return
	}
	if event.Action != "deleted" && event.Action != "suspend" {
		return
	}

	external := strconv.FormatInt(event.InstallationID, 10)
	err = p.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		return sqlc.New(tx).RevokeInstallation(ctx, external)
	})
	if err != nil {
		slog.Error("could not record access being taken away",
			"installation", external, "error", err)
		return
	}
	slog.Info("access to a repository was taken away", "installation", external, "action", event.Action)
}
