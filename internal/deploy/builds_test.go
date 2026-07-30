package deploy

import (
	"context"
	"testing"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// newDeployment queues a deployment of a service, as a push would.
func (f *fixture) newDeployment(t *testing.T, serviceID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	ref, sha := "refs/heads/main", "abcdef1234567890"

	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		_, err := sqlc.New(tx).CreateDeployment(ctx, sqlc.CreateDeploymentParams{
			ID:        id,
			OrgID:     f.orgID,
			ServiceID: serviceID,
			CommitRef: &ref,
			CommitSha: &sha,
		})
		return err
	})
	if err != nil {
		t.Fatalf("queue a deployment: %v", err)
	}
	return id
}

func (f *fixture) deployment(t *testing.T, id uuid.UUID) sqlc.Deployment {
	t.Helper()
	ctx := context.Background()

	var found sqlc.Deployment
	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		var err error
		found, err = sqlc.New(tx).GetDeployment(ctx, id)
		return err
	})
	if err != nil {
		t.Fatalf("read the deployment: %v", err)
	}
	return found
}

// The image is recorded before the rollout, so a rollback has something to run even if what
// follows goes wrong.
func TestASuccessfulBuildRecordsWhatToRun(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-built")
	id := f.newDeployment(t, service)

	NewDeployments(f.pool).FinishBuild(context.Background(), f.orgID, proto.BuildResult{
		DeploymentID: id.String(),
		Succeeded:    true,
		ImageRef:     "yol/app:abcdef1",
	})

	after := f.deployment(t, id)
	if after.ImageRef == nil || *after.ImageRef != "yol/app:abcdef1" {
		t.Errorf("image = %v, want what was built", after.ImageRef)
	}
	if after.Status != sqlc.DeploymentStatusDeploying {
		t.Errorf("status = %s, want it moving on to being deployed", after.Status)
	}
}

// A failed build keeps its reason, so the interface can say what happened without reading back
// the whole output.
func TestAFailedBuildKeepsItsReason(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-broken")
	id := f.newDeployment(t, service)

	NewDeployments(f.pool).FinishBuild(context.Background(), f.orgID, proto.BuildResult{
		DeploymentID: id.String(),
		Reason:       "The build did not finish. The output above says where it stopped.",
	})

	after := f.deployment(t, id)
	if after.Status != sqlc.DeploymentStatusFailed {
		t.Errorf("status = %s, want failed", after.Status)
	}
	if after.FailureReason == nil || *after.FailureReason == "" {
		t.Error("the failure carried no reason a user could read")
	}
}

// A build that failed with nothing to say still must not look like a build in progress forever.
func TestAFailedBuildWithNoReasonStillSaysSomething(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-silent")
	id := f.newDeployment(t, service)

	NewDeployments(f.pool).FinishBuild(context.Background(), f.orgID, proto.BuildResult{
		DeploymentID: id.String(),
	})

	after := f.deployment(t, id)
	if after.Status != sqlc.DeploymentStatusFailed || after.FailureReason == nil {
		t.Errorf("status = %s, reason = %v, want a failure with something said", after.Status, after.FailureReason)
	}
}

// A version that answered takes over, and the one it replaced steps aside. Only one is live at a
// time, which the database enforces as well.
func TestAVersionThatAnsweredTakesOver(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-rollout")
	deployments := NewDeployments(f.pool)
	ctx := context.Background()

	first := f.newDeployment(t, service)
	deployments.FinishRollout(ctx, f.orgID, proto.Rollout{Deployment: first.String(), Healthy: true})
	if status := f.deployment(t, first).Status; status != sqlc.DeploymentStatusLive {
		t.Fatalf("status = %s, want the first version live", status)
	}

	second := f.newDeployment(t, service)
	deployments.FinishRollout(ctx, f.orgID, proto.Rollout{Deployment: second.String(), Healthy: true})

	if status := f.deployment(t, second).Status; status != sqlc.DeploymentStatusLive {
		t.Errorf("status = %s, want the new version live", status)
	}
	if status := f.deployment(t, first).Status; status != sqlc.DeploymentStatusSuperseded {
		t.Errorf("status = %s, want the previous version to have stepped aside", status)
	}
}

// The whole point of the health gate: a version that never answered fails, and the one already
// serving is left exactly as it was.
func TestAVersionThatNeverAnsweredLeavesThePreviousOneServing(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-unhealthy")
	deployments := NewDeployments(f.pool)
	ctx := context.Background()

	serving := f.newDeployment(t, service)
	deployments.FinishRollout(ctx, f.orgID, proto.Rollout{Deployment: serving.String(), Healthy: true})

	broken := f.newDeployment(t, service)
	deployments.FinishRollout(ctx, f.orgID, proto.Rollout{
		Deployment: broken.String(),
		Reason:     "This version started but never began answering.",
	})

	if status := f.deployment(t, broken).Status; status != sqlc.DeploymentStatusFailed {
		t.Errorf("status = %s, want the new version failed", status)
	}
	if status := f.deployment(t, serving).Status; status != sqlc.DeploymentStatusLive {
		t.Errorf("status = %s, want the version already serving untouched", status)
	}
}

// Output is kept so a build that failed overnight can still be read in the morning.
func TestBuildOutputIsKept(t *testing.T) {
	f := newFixture(t)
	service := f.newService(t, "app-output")
	id := f.newDeployment(t, service)

	NewDeployments(f.pool).RecordBuildOutput(context.Background(), f.orgID, proto.BuildOutput{
		DeploymentID: id.String(),
		Lines: []proto.LogLine{
			{Stream: "yol", Text: "Fetching the code at abcdef1."},
			{Stream: "stdout", Text: "Step 1/4 : FROM alpine"},
		},
	})

	var kept int
	err := f.pool.InOrg(context.Background(), f.orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM deployment_logs WHERE deployment_id = $1`, id).Scan(&kept)
	})
	if err != nil {
		t.Fatalf("read the output back: %v", err)
	}
	if kept != 2 {
		t.Errorf("kept %d lines, want both", kept)
	}
}

// Output arriving for a deployment that no longer exists must not take anything down, since an
// agent can be mid-build when a project is deleted.
func TestOutputForSomethingDeletedIsHarmless(t *testing.T) {
	f := newFixture(t)

	NewDeployments(f.pool).RecordBuildOutput(context.Background(), f.orgID, proto.BuildOutput{
		DeploymentID: uuid.New().String(),
		Lines:        []proto.LogLine{{Stream: "stdout", Text: "orphaned"}},
	})
	NewDeployments(f.pool).FinishRollout(context.Background(), f.orgID, proto.Rollout{
		Deployment: uuid.New().String(),
		Healthy:    true,
	})
}
