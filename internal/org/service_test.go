package org

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/ebnsina/yol/internal/auth"
	"github.com/ebnsina/yol/internal/config"
	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type fixture struct {
	svc  *Service
	auth *auth.Service
	pool *db.Pool
}

// These tests need the local database from `make dev-db migrate-up`.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("YOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("YOL_TEST_DATABASE_URL not set")
	}
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)

	webOrigin, _ := url.Parse("http://localhost:5173")
	cfg := &config.API{Env: config.EnvDevelopment, WebOrigin: webOrigin, SessionTTL: time.Hour}
	return &fixture{svc: NewService(pool), auth: auth.NewService(pool, cfg), pool: pool}
}

// newUser creates an account and removes it, and everything cascading from it, afterwards.
func (f *fixture) newUser(t *testing.T) auth.User {
	t.Helper()
	email := "test-" + uuid.NewString()[:8] + "@test.io"

	cred, err := f.auth.Signup(context.Background(), auth.SignupInput{
		Email: email, Password: "a-long-enough-passphrase", Name: "Test Person",
	})
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	t.Cleanup(func() {
		_ = f.pool.Unscoped(context.Background(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, cred.User.ID)
			return err
		})
	})
	return cred.User
}

func (f *fixture) newOrg(t *testing.T, owner auth.User) *Organization {
	t.Helper()
	o, err := f.svc.Create(context.Background(), owner.ID, "Test Org "+uuid.NewString()[:6])
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	t.Cleanup(func() {
		_ = f.pool.Unscoped(context.Background(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, o.ID)
			return err
		})
	})
	return o
}

// join puts a user into an org by inviting and accepting, as the API does.
func (f *fixture) join(t *testing.T, owner auth.User, o *Organization, invitee auth.User, role Role) {
	t.Helper()
	ctx := context.Background()

	m, err := f.svc.Resolve(ctx, owner.ID, o.Slug)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, token, err := f.svc.Invite(ctx, m, owner.ID, invitee.Email, role)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := f.svc.Accept(ctx, token, invitee.ID, invitee.Email); err != nil {
		t.Fatalf("accept: %v", err)
	}
}

func TestCreateMakesCallerOwner(t *testing.T) {
	f := newFixture(t)
	owner := f.newUser(t)
	o := f.newOrg(t, owner)

	if o.Role != RoleOwner {
		t.Errorf("role = %q, want owner", o.Role)
	}
	if !o.Permissions.ManageOrganization || !o.Permissions.ManageMembers {
		t.Errorf("owner lacks management permissions: %+v", o.Permissions)
	}
	if o.Slug == "" {
		t.Error("no slug generated")
	}
}

// Two organizations may share a display name without colliding.
func TestCreateAllowsDuplicateNamesAcrossUsers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.newUser(t), f.newUser(t)

	first, err := f.svc.Create(ctx, a.ID, "Identical Name")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := f.svc.Create(ctx, b.ID, "Identical Name")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	t.Cleanup(func() {
		_ = f.pool.Unscoped(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM organizations WHERE id = ANY($1)`,
				[]uuid.UUID{first.ID, second.ID})
			return err
		})
	})

	if first.Slug == second.Slug {
		t.Errorf("both organizations got the slug %q", first.Slug)
	}
}

// A non-member must not be able to tell whether an organization exists.
func TestResolveHidesOrganizationsFromNonMembers(t *testing.T) {
	f := newFixture(t)
	owner, outsider := f.newUser(t), f.newUser(t)
	o := f.newOrg(t, owner)

	_, err := f.svc.Resolve(context.Background(), outsider.ID, o.Slug)
	if err == nil {
		t.Fatal("outsider resolved an organization they do not belong to")
	}
	e := httpx.AsError(err)
	if e.Code != httpx.CodeNotFound {
		t.Errorf("code = %q, want %q so existence is not revealed", e.Code, httpx.CodeNotFound)
	}
}

func TestListForUserReturnsOnlyOwnOrganizations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, other := f.newUser(t), f.newUser(t)
	mine := f.newOrg(t, owner)
	f.newOrg(t, other)

	got, err := f.svc.ListForUser(ctx, owner.ID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d organizations, want 1", len(got))
	}
	if got[0].ID != mine.ID {
		t.Error("returned an organization the user does not belong to")
	}
}

// An invitation names one address, so forwarding the link must not work.
func TestInvitationIsBoundToItsEmail(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, invitee, stranger := f.newUser(t), f.newUser(t), f.newUser(t)
	o := f.newOrg(t, owner)

	m, err := f.svc.Resolve(ctx, owner.ID, o.Slug)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, token, err := f.svc.Invite(ctx, m, owner.ID, invitee.Email, RoleMember)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	if _, err := f.svc.Accept(ctx, token, stranger.ID, stranger.Email); err == nil {
		t.Fatal("a forwarded invitation was accepted by the wrong account")
	}
	if _, err := f.svc.Accept(ctx, token, invitee.ID, invitee.Email); err != nil {
		t.Fatalf("the invited account could not accept: %v", err)
	}
}

func TestInvitationCannotBeReused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, invitee := f.newUser(t), f.newUser(t)
	o := f.newOrg(t, owner)

	m, _ := f.svc.Resolve(ctx, owner.ID, o.Slug)
	_, token, err := f.svc.Invite(ctx, m, owner.ID, invitee.Email, RoleMember)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := f.svc.Accept(ctx, token, invitee.ID, invitee.Email); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if _, err := f.svc.Accept(ctx, token, invitee.ID, invitee.Email); err == nil {
		t.Fatal("the same invitation was accepted twice")
	}
}

// Members and viewers must not be able to manage the organization.
func TestRolesCannotActBeyondTheirCapabilities(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := f.newUser(t)
	o := f.newOrg(t, owner)

	for _, role := range []Role{RoleMember, RoleViewer} {
		t.Run(string(role), func(t *testing.T) {
			user := f.newUser(t)
			f.join(t, owner, o, user, role)

			m, err := f.svc.Resolve(ctx, user.ID, o.Slug)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}

			if _, err := f.svc.Rename(ctx, m, user.ID, "Renamed"); err == nil {
				t.Error("renamed the organization without permission")
			}
			if _, _, err := f.svc.Invite(ctx, m, user.ID, "someone@test.io", RoleMember); err == nil {
				t.Error("invited someone without permission")
			}
			if err := f.svc.RemoveMember(ctx, m, user.ID, owner.ID); err == nil {
				t.Error("removed a member without permission")
			}
		})
	}
}

// An admin may manage members but must not be able to mint an owner.
func TestAdminCannotEscalateToOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, admin, target := f.newUser(t), f.newUser(t), f.newUser(t)
	o := f.newOrg(t, owner)
	f.join(t, owner, o, admin, RoleAdmin)
	f.join(t, owner, o, target, RoleMember)

	m, err := f.svc.Resolve(ctx, admin.ID, o.Slug)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if _, _, err := f.svc.Invite(ctx, m, admin.ID, "new-owner@test.io", RoleOwner); err == nil {
		t.Error("an admin invited a new owner")
	}
	if _, err := f.svc.ChangeRole(ctx, m, admin.ID, target.ID, RoleOwner); err == nil {
		t.Error("an admin promoted someone to owner")
	}
	// The permitted case still works.
	if _, err := f.svc.ChangeRole(ctx, m, admin.ID, target.ID, RoleViewer); err != nil {
		t.Errorf("an admin could not change a member role: %v", err)
	}
}

// An organization with no owner would be permanently unmanageable.
func TestLastOwnerCannotBeRemovedOrDemoted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, second := f.newUser(t), f.newUser(t)
	o := f.newOrg(t, owner)

	m, err := f.svc.Resolve(ctx, owner.ID, o.Slug)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if err := f.svc.RemoveMember(ctx, m, owner.ID, owner.ID); err == nil {
		t.Error("the only owner was removed")
	}
	if _, err := f.svc.ChangeRole(ctx, m, owner.ID, owner.ID, RoleMember); err == nil {
		t.Error("the only owner was demoted")
	}

	// With a second owner, both become possible.
	f.join(t, owner, o, second, RoleOwner)
	if err := f.svc.RemoveMember(ctx, m, owner.ID, owner.ID); err != nil {
		t.Errorf("an owner could not leave once another owner existed: %v", err)
	}
}

func TestListMembersMarksTheLastOwnerAsNotRemovable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := f.newUser(t)
	o := f.newOrg(t, owner)

	m, _ := f.svc.Resolve(ctx, owner.ID, o.Slug)
	members, err := f.svc.ListMembers(ctx, m, owner.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("got %d members, want 1", len(members))
	}
	if members[0].CanBeRemove {
		t.Error("the only owner is reported as removable, which the API would then refuse")
	}
}

func TestRevokedInvitationCannotBeAccepted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, invitee := f.newUser(t), f.newUser(t)
	o := f.newOrg(t, owner)

	m, _ := f.svc.Resolve(ctx, owner.ID, o.Slug)
	inv, token, err := f.svc.Invite(ctx, m, owner.ID, invitee.Email, RoleMember)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if err := f.svc.Revoke(ctx, m, owner.ID, inv.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := f.svc.Accept(ctx, token, invitee.ID, invitee.Email); err == nil {
		t.Fatal("a revoked invitation was accepted")
	}
}

func TestPreviewInvitationRevealsOnlyWhatIsNeeded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner, invitee := f.newUser(t), f.newUser(t)
	o := f.newOrg(t, owner)

	m, _ := f.svc.Resolve(ctx, owner.ID, o.Slug)
	_, token, err := f.svc.Invite(ctx, m, owner.ID, invitee.Email, RoleMember)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	signedOut, err := f.svc.PreviewInvitation(ctx, token, "")
	if err != nil {
		t.Fatalf("PreviewInvitation: %v", err)
	}
	if !signedOut.NeedsAuth || signedOut.Matches {
		t.Errorf("signed-out preview wrong: %+v", signedOut)
	}
	if signedOut.OrgName != o.Name {
		t.Errorf("organization name = %q, want %q", signedOut.OrgName, o.Name)
	}

	matching, err := f.svc.PreviewInvitation(ctx, token, invitee.Email)
	if err != nil {
		t.Fatalf("PreviewInvitation: %v", err)
	}
	if !matching.Matches || matching.NeedsAuth {
		t.Errorf("matching preview wrong: %+v", matching)
	}
}

func TestPreviewInvitationRejectsUnknownToken(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.PreviewInvitation(context.Background(), "not-a-real-token", ""); err == nil {
		t.Fatal("an unknown token was previewed")
	}
}

func TestPermissionsMatchCapabilities(t *testing.T) {
	cases := map[Role]Permissions{
		RoleOwner:  {ManageOrganization: true, ManageMembers: true, ManageServers: true, Deploy: true, ViewLogs: true},
		RoleAdmin:  {ManageOrganization: false, ManageMembers: true, ManageServers: true, Deploy: true, ViewLogs: true},
		RoleMember: {ManageOrganization: false, ManageMembers: false, ManageServers: false, Deploy: true, ViewLogs: true},
		RoleViewer: {ManageOrganization: false, ManageMembers: false, ManageServers: false, Deploy: false, ViewLogs: true},
	}
	for role, want := range cases {
		if got := PermissionsFor(role); got != want {
			t.Errorf("PermissionsFor(%q) = %+v, want %+v", role, got, want)
		}
	}
}

func TestRoleValidateRejectsUnknownRoles(t *testing.T) {
	for _, role := range []Role{"superuser", "", "OWNER", "root"} {
		if err := Role(role).Validate(); err == nil {
			t.Errorf("Validate accepted %q", role)
		}
	}
	for _, role := range allRoles {
		if err := role.Validate(); err != nil {
			t.Errorf("Validate rejected %q: %v", role, err)
		}
	}
}
