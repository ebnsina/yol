package deploy

import (
	"context"
	"testing"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/org"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// environmentOf creates a project and returns one of its environments to set variables on.
func (f *fixture) environmentOf(t *testing.T, projects *Projects, m *org.Membership, userID uuid.UUID) uuid.UUID {
	t.Helper()

	created, err := projects.Create(context.Background(), m, userID, "Vars "+uuid.New().String()[:6])
	if err != nil {
		t.Fatalf("create a project: %v", err)
	}
	return uuid.MustParse(created.Environments[0].ID)
}

// A value is stored so it can be handed to a server, and listed so somebody can see what is set,
// but never sent back. There is deliberately no way to read one through the API at all.
func TestAVariableIsListedButNeverReturned(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	envID := f.environmentOf(t, projects, m, userID)
	ctx := context.Background()

	if err := projects.SetVariable(ctx, m, userID, envID, "DATABASE_URL", "postgres://secret"); err != nil {
		t.Fatalf("set a variable: %v", err)
	}

	listed, err := projects.ListVariables(ctx, m, userID, envID)
	if err != nil {
		t.Fatalf("list variables: %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "DATABASE_URL" {
		t.Fatalf("listed = %+v, want the one name that was set", listed)
	}
	if listed[0].UpdatedAt.IsZero() {
		t.Error("no time was recorded, so a client cannot say when it last changed")
	}
}

// What is stored must not be readable from the database alone, since that is the whole point of
// encrypting it.
func TestWhatIsStoredIsNotThePlainValue(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	envID := f.environmentOf(t, projects, m, userID)
	ctx := context.Background()

	if err := projects.SetVariable(ctx, m, userID, envID, "API_KEY", "plain-secret-value"); err != nil {
		t.Fatal(err)
	}

	var stored []byte
	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT value FROM env_vars WHERE env_id = $1 AND name = 'API_KEY'`, envID).Scan(&stored)
	})
	if err != nil {
		t.Fatalf("read the stored value: %v", err)
	}
	if string(stored) == "plain-secret-value" {
		t.Error("the value was stored as it was given")
	}
}

// The value has to come back out for a deploy, or an app would start without it.
func TestAVariableCanBeReadBackForADeploy(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	envID := f.environmentOf(t, projects, m, userID)
	ctx := context.Background()

	if err := projects.SetVariable(ctx, m, userID, envID, "API_KEY", "plain-secret-value"); err != nil {
		t.Fatal(err)
	}

	var found map[string]string
	err := f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		var err error
		found, err = projects.variablesFor(ctx, sqlc.New(tx), envID)
		return err
	})
	if err != nil {
		t.Fatalf("read variables for a deploy: %v", err)
	}
	if found["API_KEY"] != "plain-secret-value" {
		t.Errorf("value = %q, want what was set", found["API_KEY"])
	}
}

// Setting the same name again replaces it, rather than leaving two values and a guess about which
// one an app would be started with.
func TestSettingAVariableAgainReplacesIt(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	envID := f.environmentOf(t, projects, m, userID)
	ctx := context.Background()

	if err := projects.SetVariable(ctx, m, userID, envID, "PORT", "3000"); err != nil {
		t.Fatal(err)
	}
	if err := projects.SetVariable(ctx, m, userID, envID, "PORT", "8080"); err != nil {
		t.Fatal(err)
	}

	listed, _ := projects.ListVariables(ctx, m, userID, envID)
	if len(listed) != 1 {
		t.Fatalf("listed %d variables, want one", len(listed))
	}

	var found map[string]string
	_ = f.pool.InOrg(ctx, f.orgID, func(tx pgx.Tx) error {
		var err error
		found, err = projects.variablesFor(ctx, sqlc.New(tx), envID)
		return err
	})
	if found["PORT"] != "8080" {
		t.Errorf("value = %q, want the newer one", found["PORT"])
	}
}

func TestAVariableCanBeRemoved(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	envID := f.environmentOf(t, projects, m, userID)
	ctx := context.Background()

	if err := projects.SetVariable(ctx, m, userID, envID, "OLD_FLAG", "1"); err != nil {
		t.Fatal(err)
	}
	if err := projects.DeleteVariable(ctx, m, userID, envID, "OLD_FLAG"); err != nil {
		t.Fatalf("remove a variable: %v", err)
	}

	if listed, _ := projects.ListVariables(ctx, m, userID, envID); len(listed) != 0 {
		t.Errorf("listed = %+v, want none left", listed)
	}
}

// A name a container will not carry is refused when it is set, rather than at deploy time when
// somebody is waiting.
func TestNamesThatCannotWorkAreRefused(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	envID := f.environmentOf(t, projects, m, userID)
	ctx := context.Background()

	for _, name := range []string{"", "1PORT", "MY-VAR", "MY VAR", "MY.VAR"} {
		if err := projects.SetVariable(ctx, m, userID, envID, name, "x"); err == nil {
			t.Errorf("the name %q was accepted", name)
		}
	}
	for _, name := range []string{"PORT", "_PRIVATE", "aBc123"} {
		if err := projects.SetVariable(ctx, m, userID, envID, name, "x"); err != nil {
			t.Errorf("the name %q was refused: %v", name, err)
		}
	}
}

// Someone who may only read must not be able to change what an app runs with.
func TestAViewerCannotChangeVariables(t *testing.T) {
	f := newFixture(t)
	m, _ := f.owner()
	userID := f.newUser(t)
	projects := f.projects(t)
	envID := f.environmentOf(t, projects, m, userID)
	viewer := &org.Membership{OrgID: f.orgID, Role: org.RoleViewer}

	err := projects.SetVariable(context.Background(), viewer, userID, envID, "API_KEY", "x")
	if err == nil {
		t.Error("a viewer set a variable")
	}
}

// An environment in another organization is not somewhere variables can be set, and is reported as
// missing rather than as forbidden.
func TestVariablesCannotBeSetOnAnotherOrganizationsEnvironment(t *testing.T) {
	f := newFixture(t)
	elsewhere := newFixture(t)
	projects := f.projects(t)

	theirEnv := elsewhere.environmentOf(t, projects,
		&org.Membership{OrgID: elsewhere.orgID, Role: org.RoleOwner}, elsewhere.newUser(t))

	m, _ := f.owner()
	err := projects.SetVariable(context.Background(), m, f.newUser(t), theirEnv, "API_KEY", "x")
	if err == nil {
		t.Error("a variable was set on another organization's environment")
	}
}
