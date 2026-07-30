package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/ebnsina/yol/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// owner is the caller in these tests, since creating projects needs it.
func (f *fixture) owner() (*org.Membership, uuid.UUID) {
	userID := uuid.New()
	return &org.Membership{OrgID: f.orgID, Role: org.RoleOwner}, userID
}

func (f *fixture) projects(t *testing.T) *Projects {
	t.Helper()

	box, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return NewProjects(f.pool, box)
}

// newUser inserts an account, because acting as a user is what row level security scopes by.
func (f *fixture) newUser(t *testing.T) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()

	err := f.pool.Tx(ctx, db.Scope{OrgID: f.orgID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO users (id, email, password_hash, name)
			 VALUES ($1, $2, 'x', 'Test')`, id, id.String()[:8]+"@example.com"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO memberships (id, org_id, user_id, role) VALUES ($1, $2, $3, 'owner')`,
			uuid.New(), f.orgID, id)
		return err
	})
	if err != nil {
		t.Fatalf("seed a user: %v", err)
	}
	return id
}

// Somewhere to try a change before it reaches the public is the normal case, so both environments
// exist from the start rather than waiting to be asked for.
func TestANewProjectHasSomewhereToTryChanges(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)

	created, err := f.projects(t).Create(context.Background(), m, userID, "Shop")
	if err != nil {
		t.Fatalf("create a project: %v", err)
	}

	if len(created.Environments) != 2 {
		t.Fatalf("environments = %d, want production and staging", len(created.Environments))
	}
	names := created.Environments[0].Name + " " + created.Environments[1].Name
	if !strings.Contains(names, "production") || !strings.Contains(names, "staging") {
		t.Errorf("environments = %s, want production and staging", names)
	}

	for _, environment := range created.Environments {
		if len(environment.Services) != 1 {
			t.Errorf("%s holds %d services, want the app", environment.Name, len(environment.Services))
		}
		if environment.ServerID != nil {
			t.Errorf("%s was assigned a server nobody chose", environment.Name)
		}
	}
}

// Each environment follows its own branch, which is what makes pushing to one deploy to one.
func TestEnvironmentsFollowDifferentBranches(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)

	created, err := f.projects(t).Create(context.Background(), m, userID, "Shop")
	if err != nil {
		t.Fatal(err)
	}

	branches := map[string]string{}
	for _, environment := range created.Environments {
		branches[environment.Name] = environment.Branch
	}
	if branches["production"] == branches["staging"] {
		t.Errorf("both environments follow %s, so a push would deploy to both", branches["production"])
	}
}

func TestAProjectNeedsAName(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)

	if _, err := f.projects(t).Create(context.Background(), m, userID, "   "); err == nil {
		t.Error("a project with no name was created")
	}
}

// Someone who may only read must not be able to create anything.
func TestAViewerCannotCreateAProject(t *testing.T) {
	f := newFixture(t)
	userID := f.newUser(t)
	viewer := &org.Membership{OrgID: f.orgID, Role: org.RoleViewer}

	if _, err := f.projects(t).Create(context.Background(), viewer, userID, "Shop"); err == nil {
		t.Error("a viewer created a project")
	}
}

// An environment is useless until it has somewhere to run, so assigning a server is the step that
// makes a project deployable.
func TestAnEnvironmentIsGivenAServerToRunOn(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	ctx := context.Background()

	created, err := projects.Create(ctx, m, userID, "Shop")
	if err != nil {
		t.Fatal(err)
	}
	envID := uuid.MustParse(created.Environments[0].ID)

	updated, err := projects.UpdateEnvironment(ctx, m, userID, envID, EnvironmentChanges{
		ServerID: &f.serverID,
	})
	if err != nil {
		t.Fatalf("assign a server: %v", err)
	}
	if updated.ServerID == nil || *updated.ServerID != f.serverID.String() {
		t.Errorf("serverId = %v, want the server chosen", updated.ServerID)
	}
}

// Assigning a server that is not this organization's must report that it was not found rather than
// that it exists somewhere else.
func TestAServerFromAnotherOrganizationCannotBeAssigned(t *testing.T) {
	f := newFixture(t)
	elsewhere := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	ctx := context.Background()

	created, err := projects.Create(ctx, m, userID, "Shop")
	if err != nil {
		t.Fatal(err)
	}
	envID := uuid.MustParse(created.Environments[0].ID)

	_, err = projects.UpdateEnvironment(ctx, m, userID, envID, EnvironmentChanges{
		ServerID: &elsewhere.serverID,
	})
	if err == nil {
		t.Fatal("a server belonging to another organization was assigned")
	}

	var failure *httpx.Error
	if !errors.As(err, &failure) || failure.Code != httpx.CodeNotFound {
		t.Errorf("error = %v, want it reported as not found rather than as forbidden", err)
	}
}

// A project from another organization is not visible at all, which row level security is what
// makes true rather than a check written in one place and forgotten in another.
func TestAProjectInAnotherOrganizationIsNotFound(t *testing.T) {
	f := newFixture(t)
	elsewhere := newFixture(t)
	projects := f.projects(t)
	ctx := context.Background()

	theirs, err := projects.Create(ctx, &org.Membership{OrgID: elsewhere.orgID, Role: org.RoleOwner},
		elsewhere.newUser(t), "Theirs")
	if err != nil {
		t.Fatal(err)
	}

	m, _ := f.owner()
	if _, err := projects.Get(ctx, m, f.newUser(t), uuid.MustParse(theirs.ID)); err == nil {
		t.Error("a project from another organization was readable")
	}
}

// How a service is checked is what the rollout waits on, so it has to be changeable.
func TestHowAServiceIsCheckedCanBeChanged(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	ctx := context.Background()

	created, err := projects.Create(ctx, m, userID, "Shop")
	if err != nil {
		t.Fatal(err)
	}
	serviceID := uuid.MustParse(created.Environments[0].Services[0].ID)

	path, port := "/healthz", 3000
	updated, err := projects.UpdateService(ctx, m, userID, serviceID, ServiceChanges{
		HealthPath: &path,
		HealthPort: &port,
	})
	if err != nil {
		t.Fatalf("change the service: %v", err)
	}
	if updated.HealthPath == nil || *updated.HealthPath != "/healthz" {
		t.Errorf("healthPath = %v, want /healthz", updated.HealthPath)
	}
	if updated.HealthPort == nil || *updated.HealthPort != 3000 {
		t.Errorf("healthPort = %v, want 3000", updated.HealthPort)
	}
}

func TestSettingsThatCannotWorkAreRefused(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	ctx := context.Background()

	created, err := projects.Create(ctx, m, userID, "Shop")
	if err != nil {
		t.Fatal(err)
	}
	serviceID := uuid.MustParse(created.Environments[0].Services[0].ID)

	relative := "healthz"
	if _, err := projects.UpdateService(ctx, m, userID, serviceID, ServiceChanges{HealthPath: &relative}); err == nil {
		t.Error("a health path that is not a path was accepted")
	}

	impossible := 70000
	if _, err := projects.UpdateService(ctx, m, userID, serviceID, ServiceChanges{HealthPort: &impossible}); err == nil {
		t.Error("a port outside the possible range was accepted")
	}

	tiny := int64(1024)
	if _, err := projects.UpdateService(ctx, m, userID, serviceID, ServiceChanges{MemoryLimitBytes: &tiny}); err == nil {
		t.Error("a memory limit nothing could run in was accepted")
	}
}
