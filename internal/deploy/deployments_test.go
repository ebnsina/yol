package deploy

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/github"
	"github.com/ebnsina/yol/internal/org"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// recordingAgents stands in for the servers, so a deploy can be exercised without one.
type recordingAgents struct {
	builds     []proto.BuildRequest
	reconciled []uuid.UUID
	failBuild  error
}

func (a *recordingAgents) Build(_ context.Context, serverID uuid.UUID, req proto.BuildRequest) error {
	if a.failBuild != nil {
		return a.failBuild
	}
	a.builds = append(a.builds, req)
	return nil
}

func (a *recordingAgents) Reconcile(_ context.Context, serverID uuid.UUID) error {
	a.reconciled = append(a.reconciled, serverID)
	return nil
}

// deployable is a project with a repository connected and a server assigned, which is the state a
// project has to reach before anything can be deployed.
func (f *fixture) deployable(t *testing.T, projects *Projects) (*org.Membership, uuid.UUID, DeployTarget) {
	t.Helper()
	ctx := context.Background()
	m, _ := f.owner()
	userID := f.newUser(t)

	created, err := projects.Create(ctx, m, userID, "Shop "+uuid.New().String()[:6])
	if err != nil {
		t.Fatalf("create a project: %v", err)
	}
	envID := uuid.MustParse(created.Environments[0].ID)

	if _, err := projects.UpdateEnvironment(ctx, m, userID, envID, EnvironmentChanges{
		ServerID: &f.serverID,
	}); err != nil {
		t.Fatalf("assign a server: %v", err)
	}

	// Recorded directly, since granting access on GitHub is not something a test can do. The
	// identifier is unique to this fixture because an installation belongs to one organization and
	// the column says so, which a shared value would trip over.
	// Numeric, as GitHub's own identifiers are, since the code parses them as numbers.
	installation := strconv.FormatUint(uint64(binary.BigEndian.Uint32(f.orgID[:4])), 10)
	repository := strconv.FormatUint(uint64(binary.BigEndian.Uint32(f.orgID[4:8])), 10)
	err = f.pool.InOrgAsUser(ctx, f.orgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.CreateInstallation(ctx, sqlc.CreateInstallationParams{
			ID:         uuid.New(),
			OrgID:      f.orgID,
			ExternalID: installation,
			Account:    "someone",
		}); err != nil {
			return err
		}
		provider, fullName := "github", "owner/shop"
		_, err := q.SetProjectRepository(ctx, sqlc.SetProjectRepositoryParams{
			ID:                 uuid.MustParse(created.ID),
			RepoProvider:       &provider,
			RepoFullName:       &fullName,
			RepoExternalID:     &repository,
			RepoInstallationID: &installation,
		})
		return err
	})
	if err != nil {
		t.Fatalf("connect a repository: %v", err)
	}

	target, _, err := projects.targetFor(ctx, m, userID, envID)
	if err != nil {
		t.Fatalf("read the deploy target: %v", err)
	}
	return m, userID, *target
}

// What the server is handed has to be enough to build without asking anything else: where the code
// is, which commit, a credential, and what the image will be called.
func TestADeployHandsTheServerEverythingItNeeds(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	agents := &recordingAgents{}
	projects.SetAgents(agents)
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	deploymentID, err := projects.Deploy(ctx, target, "abcdef1234567890", "main")
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	if len(agents.builds) != 1 {
		t.Fatalf("%d builds were asked for, want one", len(agents.builds))
	}
	request := agents.builds[0]

	if request.DeploymentID != deploymentID.String() {
		t.Errorf("deploymentId = %q, want the deployment recorded", request.DeploymentID)
	}
	if request.CommitSHA != "abcdef1234567890" {
		t.Errorf("commit = %q, want the one asked for", request.CommitSHA)
	}
	if request.SourceToken != "ghs_test" {
		t.Errorf("token = %q, want the one minted for this deploy", request.SourceToken)
	}
	if request.SourceURL == "" || request.ImageRef == "" {
		t.Errorf("request = %+v, want somewhere to fetch from and something to call the image", request)
	}
	if request.MemoryLimitBytes == 0 || request.CPUPercent == 0 {
		t.Error("the build was given no limits, so it could take the whole machine")
	}

	// The deployment reads as building rather than sitting as though nothing happened.
	deployment, err := projects.GetDeployment(ctx, m, userID, deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Status != string(sqlc.DeploymentStatusBuilding) {
		t.Errorf("status = %s, want building", deployment.Status)
	}
}

// Where a version will run is written down before the build starts, so the desired state already
// names it by the time an image exists.
func TestWhereAVersionWillRunIsWrittenDownFirst(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	deploymentID, err := projects.Deploy(ctx, target, "abcdef1234567890", "main")
	if err != nil {
		t.Fatal(err)
	}

	var placements []sqlc.Placement
	err = f.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		var err error
		placements, err = sqlc.New(tx).ListPlacements(ctx, deploymentID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) != 1 {
		t.Fatalf("%d placements, want one", len(placements))
	}
	if placements[0].ContainerName != ContainerNameFor(target.ServiceID, deploymentID) {
		t.Errorf("container = %q, want it named for this deployment", placements[0].ContainerName)
	}
}

// A server that cannot be reached must leave the deployment marked failed rather than in progress
// forever, waiting for a report that will never arrive.
func TestADeployToAnUnreachableServerFails(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{failBuild: errNoCode})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	if _, err := projects.Deploy(ctx, target, "abcdef1234567890", "main"); err == nil {
		t.Fatal("a deploy to a server that could not be reached reported success")
	}

	deployments, err := projects.ListDeployments(ctx, m, userID, target.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 {
		t.Fatalf("%d deployments, want the attempt recorded", len(deployments))
	}
	if deployments[0].Status != string(sqlc.DeploymentStatusFailed) {
		t.Errorf("status = %s, want failed", deployments[0].Status)
	}
	if deployments[0].FailureReason == nil {
		t.Error("the failure carried no reason a user could read")
	}
}

// An environment with no server is not deployable, and saying so is more use than a deploy that
// goes nowhere.
func TestAnEnvironmentWithNoServerCannotBeDeployed(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	_, _, target := f.deployable(t, projects)
	target.ServerID = nil

	if _, err := projects.Deploy(context.Background(), target, "abcdef1234567890", "main"); err == nil {
		t.Error("a deploy with nowhere to run was accepted")
	}
}

// Rolling back runs an image already on the machine, so nothing is built and it takes seconds.
func TestRollingBackBuildsNothing(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	agents := &recordingAgents{}
	projects.SetAgents(agents)
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	deployments := NewDeployments(f.pool)
	ctx := context.Background()

	// A version that was built and served, then replaced by another.
	first, err := projects.Deploy(ctx, target, "aaaaaaaaaaaa", "main")
	if err != nil {
		t.Fatal(err)
	}
	deployments.FinishBuild(ctx, m.OrgID, proto.BuildResult{
		DeploymentID: first.String(), Succeeded: true, ImageRef: "yol/app:aaaaaaaaaaaa",
	})
	deployments.FinishRollout(ctx, m.OrgID, proto.Rollout{Deployment: first.String(), Healthy: true})

	second, err := projects.Deploy(ctx, target, "bbbbbbbbbbbb", "main")
	if err != nil {
		t.Fatal(err)
	}
	deployments.FinishBuild(ctx, m.OrgID, proto.BuildResult{
		DeploymentID: second.String(), Succeeded: true, ImageRef: "yol/app:bbbbbbbbbbbb",
	})
	deployments.FinishRollout(ctx, m.OrgID, proto.Rollout{Deployment: second.String(), Healthy: true})

	builtBefore := len(agents.builds)
	back, err := projects.Rollback(ctx, m, userID, first)
	if err != nil {
		t.Fatalf("roll back: %v", err)
	}

	if len(agents.builds) != builtBefore {
		t.Error("rolling back started a build, when the image is already on the machine")
	}
	if back.ImageRef == nil || *back.ImageRef != "yol/app:aaaaaaaaaaaa" {
		t.Errorf("image = %v, want the one the old version produced", back.ImageRef)
	}
	if back.Status != string(sqlc.DeploymentStatusDeploying) {
		t.Errorf("status = %s, want it going out", back.Status)
	}
	if len(agents.reconciled) == 0 {
		t.Error("the server was never told, so the rollback would not happen")
	}
}

// The history has to read as what actually happened, so a rollback is a new attempt rather than an
// old one coming back to life.
func TestRollingBackRecordsANewAttempt(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	deployments := NewDeployments(f.pool)
	ctx := context.Background()

	first, _ := projects.Deploy(ctx, target, "aaaaaaaaaaaa", "main")
	deployments.FinishBuild(ctx, m.OrgID, proto.BuildResult{
		DeploymentID: first.String(), Succeeded: true, ImageRef: "yol/app:aaaaaaaaaaaa",
	})

	back, err := projects.Rollback(ctx, m, userID, first)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID == first.String() {
		t.Error("the old deployment was revived rather than a new one recorded")
	}

	history, err := projects.ListDeployments(ctx, m, userID, target.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Errorf("%d deployments in the history, want the original and the rollback", len(history))
	}
}

// Going back to something that was never built has nothing to run, and starting a rollout of
// nothing would take the working version away.
func TestRollingBackToSomethingNeverBuiltIsRefused(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	ctx := context.Background()

	never, err := projects.Deploy(ctx, target, "cccccccccccc", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.Rollback(ctx, m, userID, never); err == nil {
		t.Error("a rollback to a version that was never built was accepted")
	}
}

// Rolling back to the version already serving is a no-op somebody would wait through, so it is
// refused with something to read instead.
func TestRollingBackToWhatIsAlreadyServingIsRefused(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	deployments := NewDeployments(f.pool)
	ctx := context.Background()

	live, _ := projects.Deploy(ctx, target, "dddddddddddd", "main")
	deployments.FinishBuild(ctx, m.OrgID, proto.BuildResult{
		DeploymentID: live.String(), Succeeded: true, ImageRef: "yol/app:dddddddddddd",
	})
	deployments.FinishRollout(ctx, m.OrgID, proto.Rollout{Deployment: live.String(), Healthy: true})

	if _, err := projects.Rollback(ctx, m, userID, live); err == nil {
		t.Error("a rollback to the version already serving was accepted")
	}
}

// Someone who may only read must not be able to deploy or roll back.
func TestAViewerCannotDeployOrRollBack(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	_, userID, target := f.deployable(t, projects)
	viewer := &org.Membership{OrgID: f.orgID, Role: org.RoleViewer}
	ctx := context.Background()

	if _, err := projects.DeployEnvironment(ctx, viewer, userID, target.EnvironmentID); err == nil {
		t.Error("a viewer deployed")
	}
	if _, err := projects.Rollback(ctx, viewer, userID, uuid.New()); err == nil {
		t.Error("a viewer rolled back")
	}
}

// Following a build means asking again from the last line held, so nothing is sent twice.
func TestLogsCanBeFollowedFromWhereTheyWereLeft(t *testing.T) {
	f := newFixture(t)
	projects := f.projects(t)
	projects.SetAgents(&recordingAgents{})
	projects.SetCode(&fakeCode{token: "ghs_test"})

	m, userID, target := f.deployable(t, projects)
	deployments := NewDeployments(f.pool)
	ctx := context.Background()

	deploymentID, _ := projects.Deploy(ctx, target, "abcdef123456", "main")
	deployments.RecordBuildOutput(ctx, m.OrgID, proto.BuildOutput{
		DeploymentID: deploymentID.String(),
		Lines:        []proto.LogLine{{Stream: "yol", Text: "first"}},
	})

	first, err := projects.DeploymentLogs(ctx, m, userID, deploymentID, time.Time{})
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("nothing was returned, so a build cannot be watched")
	}

	deployments.RecordBuildOutput(ctx, m.OrgID, proto.BuildOutput{
		DeploymentID: deploymentID.String(),
		Lines:        []proto.LogLine{{Stream: "stdout", Text: "second"}},
	})

	next, err := projects.DeploymentLogs(ctx, m, userID, deploymentID, first[len(first)-1].At)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range next {
		if line.Text == "first" {
			t.Error("a line already held was sent again")
		}
	}
	if len(next) != 1 || next[0].Text != "second" {
		t.Errorf("lines = %+v, want only what arrived since", next)
	}
}

// fakeCode stands in for GitHub.
type fakeCode struct {
	token  string
	commit string
}

func (c *fakeCode) InstallURL() string { return "https://github.com/apps/yol/installations/new" }

func (c *fakeCode) InstallationToken(context.Context, int64) (string, error) {
	return c.token, nil
}

func (c *fakeCode) LatestCommit(context.Context, string, string, string) (string, error) {
	if c.commit == "" {
		return "abcdef1234567890", nil
	}
	return c.commit, nil
}

func (c *fakeCode) Installation(context.Context, int64) (*github.Installation, error) {
	return &github.Installation{ID: 42, Account: "someone"}, nil
}

func (c *fakeCode) Repositories(context.Context, int64) ([]github.Repository, error) {
	return nil, nil
}

func (c *fakeCode) SourceURL(fullName, commitSHA string) string {
	return "https://api.github.test/repos/" + fullName + "/tarball/" + commitSHA
}
