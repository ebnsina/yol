package deploy

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Build output is kept so a failure can be read after the fact rather than only while watching.
// A build that failed at four in the morning is the one a user most wants to read.

// Deployments keeps what happened while deploying. The building and the rollout both happen on the
// customer's own machine; this only records what came back.
type Deployments struct {
	pool   *db.Pool
	agents Agents
}

// NewDeployments builds the recorder.
func NewDeployments(pool *db.Pool) *Deployments {
	return &Deployments{pool: pool}
}

// SetAgents gives the recorder a way to set a rollout going once an image exists.
func (d *Deployments) SetAgents(agents Agents) { d.agents = agents }

// RecordBuildOutput stores a batch of output. Failures are logged and dropped rather than
// returned: losing a line of output must never be what fails a deploy.
func (d *Deployments) RecordBuildOutput(ctx context.Context, orgID uuid.UUID, output proto.BuildOutput) {
	deploymentID, err := uuid.Parse(output.DeploymentID)
	if err != nil || len(output.Lines) == 0 {
		return
	}

	err = d.pool.InOrg(ctx, orgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		for _, line := range output.Lines {
			if err := q.AppendDeploymentLog(ctx, sqlc.AppendDeploymentLogParams{
				ID:           uuid.New(),
				OrgID:        orgID,
				DeploymentID: deploymentID,
				Stream:       line.Stream,
				Text:         line.Text,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Warn("could not store build output", "deployment", deploymentID, "error", err)
	}
}

// FinishBuild records how a build ended. A failure keeps its reason on the deployment, so the
// interface has something to show without reading back the whole output.
func (d *Deployments) FinishBuild(ctx context.Context, orgID uuid.UUID, result proto.BuildResult) {
	deploymentID, err := uuid.Parse(result.DeploymentID)
	if err != nil {
		return
	}

	var servers []uuid.UUID
	err = d.pool.InOrg(ctx, orgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)

		if !result.Succeeded {
			reason := result.Reason
			if reason == "" {
				reason = "The build did not finish, and we did not learn why."
			}
			return q.SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
				ID:            deploymentID,
				Status:        sqlc.DeploymentStatusFailed,
				FailureReason: &reason,
			})
		}

		// The image is recorded before the rollout, so a rollback later has something to run
		// even if what follows fails.
		if err := q.SetDeploymentImage(ctx, sqlc.SetDeploymentImageParams{
			ID:       deploymentID,
			ImageRef: &result.ImageRef,
		}); err != nil {
			return err
		}
		if err := q.SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
			ID:     deploymentID,
			Status: sqlc.DeploymentStatusDeploying,
		}); err != nil {
			return err
		}

		placements, err := q.ListPlacements(ctx, deploymentID)
		if err != nil {
			return err
		}
		for _, placement := range placements {
			servers = append(servers, placement.ServerID)
		}
		return nil
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("could not record how a build ended", "deployment", deploymentID, "error", err)
		return
	}

	// The image exists, so the server is handed its desired state again and the rollout follows
	// from that rather than from a second instruction. A server that is not reachable picks the
	// change up on its next pass, which is the same path a reboot takes.
	if d.agents == nil {
		return
	}
	for _, serverID := range servers {
		if err := d.agents.Reconcile(ctx, serverID); err != nil {
			slog.Warn("could not set a rollout going", "server", serverID, "error", err)
		}
	}
}

// FinishRollout records whether a version began serving. Only one deployment of a service is live
// at a time, so the previous one steps aside as this one takes over.
func (d *Deployments) FinishRollout(ctx context.Context, orgID uuid.UUID, rollout proto.Rollout) {
	deploymentID, err := uuid.Parse(rollout.Deployment)
	if err != nil {
		return
	}

	err = d.pool.InOrg(ctx, orgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)

		if !rollout.Healthy {
			reason := rollout.Reason
			if reason == "" {
				reason = "This version never began answering, so the previous one is still serving."
			}
			return q.SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
				ID:            deploymentID,
				Status:        sqlc.DeploymentStatusFailed,
				FailureReason: &reason,
			})
		}

		deployment, err := q.GetDeployment(ctx, deploymentID)
		if err != nil {
			return err
		}
		// The previous one steps aside first, because a service is allowed only one live
		// deployment and that is checked as each statement runs. Both happen in one transaction,
		// so no reader ever sees the moment in between.
		if err := q.SupersedePreviousDeployments(ctx, sqlc.SupersedePreviousDeploymentsParams{
			ServiceID: deployment.ServiceID,
			ID:        deploymentID,
		}); err != nil {
			return err
		}
		return q.SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
			ID:     deploymentID,
			Status: sqlc.DeploymentStatusLive,
		})
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("could not record how a rollout ended", "deployment", deploymentID, "error", err)
	}
}
