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

// Builds keeps what happened during a build. The building itself happens on the customer's own
// machine; this only records it.
type Builds struct {
	pool *db.Pool
}

// NewBuilds builds the recorder.
func NewBuilds(pool *db.Pool) *Builds {
	return &Builds{pool: pool}
}

// RecordBuildOutput stores a batch of output. Failures are logged and dropped rather than
// returned: losing a line of output must never be what fails a deploy.
func (b *Builds) RecordBuildOutput(ctx context.Context, orgID uuid.UUID, output proto.BuildOutput) {
	deploymentID, err := uuid.Parse(output.DeploymentID)
	if err != nil || len(output.Lines) == 0 {
		return
	}

	err = b.pool.InOrg(ctx, orgID, func(tx pgx.Tx) error {
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
func (b *Builds) FinishBuild(ctx context.Context, orgID uuid.UUID, result proto.BuildResult) {
	deploymentID, err := uuid.Parse(result.DeploymentID)
	if err != nil {
		return
	}

	err = b.pool.InOrg(ctx, orgID, func(tx pgx.Tx) error {
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
		return q.SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
			ID:     deploymentID,
			Status: sqlc.DeploymentStatusDeploying,
		})
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("could not record how a build ended", "deployment", deploymentID, "error", err)
	}
}
