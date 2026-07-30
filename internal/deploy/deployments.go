package deploy

import (
	"context"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Deploying by hand is the same act as a push arriving: the head of the environment's branch is
// looked up and built. Rolling back is not, and is the reason images are kept on the machine: it
// runs one that is already there rather than building the old commit again.

// Deployment is what a client is shown about one attempt.
type Deployment struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// The commit deployed. Absent on nothing, but shortened by whoever displays it.
	CommitSHA *string `json:"commitSha"`
	CommitRef *string `json:"commitRef"`
	ImageRef  *string `json:"imageRef"`
	// Plain language, set when something went wrong, so a client shows this rather than guessing
	// from the status.
	FailureReason *string    `json:"failureReason"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
}

// LogLine is one line of what a deploy printed.
type LogLine struct {
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// How many deployments and log lines are returned at once. Enough to fill a screen and follow a
// build, without letting one request read back everything ever deployed.
const (
	deploymentsPerRequest = 50
	logLinesPerRequest    = 2000
)

// DeployEnvironment builds and rolls out the head of the branch an environment follows.
func (p *Projects) DeployEnvironment(
	ctx context.Context,
	m *org.Membership,
	userID, envID uuid.UUID,
) (*Deployment, error) {
	if err := m.Role.Require(org.CanDeploy); err != nil {
		return nil, err
	}
	if p.code == nil {
		return nil, httpx.Internal(errNoCode)
	}

	target, branch, err := p.targetFor(ctx, m, userID, envID)
	if err != nil {
		return nil, err
	}
	if target.RepoFullName == nil || target.InstallationID == nil {
		return nil, httpx.InvalidInput("Connect a repository to this project before deploying.").
			WithField("repository", "None is connected yet.")
	}

	// The head of the branch, so pressing deploy means the same thing as pushing to it.
	commit, err := p.code.LatestCommit(ctx, *target.InstallationID, *target.RepoFullName, branch)
	if err != nil {
		return nil, httpx.InvalidInput("We could not find that branch on GitHub.").WithCause(err)
	}

	deploymentID, err := p.Deploy(ctx, *target, commit, branch)
	if err != nil {
		return nil, err
	}
	return p.GetDeployment(ctx, m, userID, deploymentID)
}

// Rollback puts a previous deployment back in front of people.
//
// No build happens: the image it produced is still on the machine, which is why a few are kept.
// A new deployment is recorded rather than the old one being revived, so the history reads as what
// actually happened rather than as a version that came back to life.
func (p *Projects) Rollback(
	ctx context.Context,
	m *org.Membership,
	userID, deploymentID uuid.UUID,
) (*Deployment, error) {
	if err := m.Role.Require(org.CanDeploy); err != nil {
		return nil, err
	}
	if p.agents == nil {
		return nil, httpx.Internal(errNoCode)
	}

	var (
		out      *Deployment
		serverID uuid.UUID
	)
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		previous, err := q.GetDeployment(ctx, deploymentID)
		if err != nil {
			return notFoundOr(err, "deployment")
		}
		if previous.ImageRef == nil {
			return httpx.InvalidInput("That version was never built, so there is nothing to go back to.")
		}
		if previous.Status == sqlc.DeploymentStatusLive {
			return httpx.InvalidInput("That version is the one already serving.")
		}

		// Where the previous one ran. A rollback goes to the same server, since that is where the
		// image it needs still is.
		placements, err := q.ListPlacements(ctx, deploymentID)
		if err != nil {
			return httpx.Internal(err)
		}
		if len(placements) == 0 {
			return httpx.InvalidInput("That version was never placed on a server.")
		}
		serverID = placements[0].ServerID

		newID := uuid.New()
		created, err := q.CreateDeployment(ctx, sqlc.CreateDeploymentParams{
			ID:        newID,
			OrgID:     m.OrgID,
			ServiceID: previous.ServiceID,
			CommitRef: previous.CommitRef,
			CommitSha: previous.CommitSha,
		})
		if err != nil {
			return httpx.Internal(err)
		}

		// The same image, so there is nothing to build and the rollout can begin at once.
		if err := q.SetDeploymentImage(ctx, sqlc.SetDeploymentImageParams{
			ID:       newID,
			ImageRef: previous.ImageRef,
		}); err != nil {
			return httpx.Internal(err)
		}
		if err := q.SetDeploymentStatus(ctx, sqlc.SetDeploymentStatusParams{
			ID:     newID,
			Status: sqlc.DeploymentStatusDeploying,
		}); err != nil {
			return httpx.Internal(err)
		}
		if err := q.SetDeploymentReplaced(ctx, sqlc.SetDeploymentReplacedParams{
			ID:                   newID,
			ReplacedDeploymentID: &deploymentID,
		}); err != nil {
			return httpx.Internal(err)
		}
		if _, err := q.CreatePlacement(ctx, sqlc.CreatePlacementParams{
			ID:            uuid.New(),
			OrgID:         m.OrgID,
			DeploymentID:  newID,
			ServerID:      serverID,
			ContainerName: ContainerNameFor(previous.ServiceID, newID),
		}); err != nil {
			return httpx.Internal(err)
		}

		created.ImageRef = previous.ImageRef
		created.Status = sqlc.DeploymentStatusDeploying
		out = toDeployment(created)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Handing the server its desired state is what starts the rollout, and it is health-gated like
	// any other, so a version that no longer answers does not take the current one down with it.
	if err := p.agents.Reconcile(ctx, serverID); err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// ListDeployments returns the recent history of one service.
func (p *Projects) ListDeployments(
	ctx context.Context,
	m *org.Membership,
	userID, serviceID uuid.UUID,
) ([]Deployment, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []Deployment{}
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetService(ctx, serviceID); err != nil {
			return notFoundOr(err, "service")
		}

		rows, err := q.ListDeployments(ctx, sqlc.ListDeploymentsParams{
			ServiceID: serviceID,
			Limit:     deploymentsPerRequest,
		})
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, *toDeployment(row))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetDeployment returns one.
func (p *Projects) GetDeployment(
	ctx context.Context,
	m *org.Membership,
	userID, deploymentID uuid.UUID,
) (*Deployment, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	var out *Deployment
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		row, err := sqlc.New(tx).GetDeployment(ctx, deploymentID)
		if err != nil {
			return notFoundOr(err, "deployment")
		}
		out = toDeployment(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeploymentLogs returns what a deploy printed, from a point onwards so a client can follow one as
// it happens without reading the whole thing again each time.
func (p *Projects) DeploymentLogs(
	ctx context.Context,
	m *org.Membership,
	userID, deploymentID uuid.UUID,
	since time.Time,
) ([]LogLine, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []LogLine{}
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetDeployment(ctx, deploymentID); err != nil {
			return notFoundOr(err, "deployment")
		}

		rows, err := q.ListDeploymentLogs(ctx, sqlc.ListDeploymentLogsParams{
			DeploymentID: deploymentID,
			At:           pgtype.Timestamptz{Time: since, Valid: true},
			Limit:        logLinesPerRequest,
		})
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, LogLine{Stream: row.Stream, Text: row.Text, At: row.At.Time})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// targetFor assembles what a deploy needs, and the branch the environment follows.
func (p *Projects) targetFor(
	ctx context.Context,
	m *org.Membership,
	userID, envID uuid.UUID,
) (*DeployTarget, string, error) {
	var (
		target DeployTarget
		branch string
	)
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		row, err := sqlc.New(tx).GetDeployTarget(ctx, envID)
		if err != nil {
			return notFoundOr(err, "environment")
		}
		target = DeployTarget{
			OrgID:          row.OrgID,
			ProjectID:      row.ProjectID,
			EnvironmentID:  row.EnvironmentID,
			ServiceID:      row.ServiceID,
			ServerID:       row.ServerID,
			RepoFullName:   row.RepoFullName,
			InstallationID: row.RepoInstallationID,
		}
		branch = row.Branch
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return &target, branch, nil
}

func toDeployment(row sqlc.Deployment) *Deployment {
	deployment := &Deployment{
		ID:            row.ID.String(),
		Status:        string(row.Status),
		CommitSHA:     row.CommitSha,
		CommitRef:     row.CommitRef,
		ImageRef:      row.ImageRef,
		FailureReason: row.FailureReason,
		CreatedAt:     row.CreatedAt.Time,
	}
	if row.StartedAt.Valid {
		at := row.StartedAt.Time
		deployment.StartedAt = &at
	}
	if row.FinishedAt.Valid {
		at := row.FinishedAt.Time
		deployment.FinishedAt = &at
	}
	return deployment
}
