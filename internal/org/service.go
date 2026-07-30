// Package org owns organizations, membership roles and invitations.
package org

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// InvitationTTL is how long an invitation link stays usable.
const InvitationTTL = 7 * 24 * time.Hour

const maxOrgNameLength = 60

// Service owns the organization rules.
type Service struct {
	pool *db.Pool
}

// NewService builds the organization service.
func NewService(pool *db.Pool) *Service {
	return &Service{pool: pool}
}

// Organization is an organization as clients see it, including what the caller may do.
// Clients render these flags rather than deciding permissions themselves.
type Organization struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Slug        string      `json:"slug"`
	Role        Role        `json:"role"`
	Permissions Permissions `json:"permissions"`
	CreatedAt   time.Time   `json:"createdAt"`
}

// Member is a person in an organization.
type Member struct {
	UserID      uuid.UUID `json:"userId"`
	Email       string    `json:"email"`
	Name        string    `json:"name"`
	Role        Role      `json:"role"`
	JoinedAt    time.Time `json:"joinedAt"`
	CanBeRemove bool      `json:"canBeRemoved"`
}

// Invitation is a pending invitation.
type Invitation struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	ExpiresAt time.Time `json:"expiresAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// InvitationPreview is what someone sees before accepting, without exposing tenant data.
type InvitationPreview struct {
	OrgName   string `json:"organizationName"`
	OrgSlug   string `json:"organizationSlug"`
	Email     string `json:"email"`
	Role      Role   `json:"role"`
	Matches   bool   `json:"matchesCurrentAccount"`
	NeedsAuth bool   `json:"needsSignIn"`
}

// Create makes an organization with the caller as its owner.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, name string) (*Organization, error) {
	name = strings.TrimSpace(name)
	if err := validateOrgName(name); err != nil {
		return nil, err
	}

	orgID := uuid.New()
	var out *Organization

	err := s.pool.Tx(ctx, db.Scope{OrgID: orgID, UserID: userID}, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.CreateOrganization(ctx, sqlc.CreateOrganizationParams{
			ID: orgID, Name: name, Slug: slugify(name, orgID),
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.AlreadyExists("You already have an organization with that name.").
					WithField("name", "Please choose a different name.")
			}
			return httpx.Internal(err)
		}

		if _, err := q.CreateMembership(ctx, sqlc.CreateMembershipParams{
			ID: uuid.New(), OrgID: orgID, UserID: userID, Role: sqlc.MembershipRoleOwner,
		}); err != nil {
			return httpx.Internal(err)
		}

		out = &Organization{
			ID: row.ID, Name: row.Name, Slug: row.Slug,
			Role: RoleOwner, Permissions: PermissionsFor(RoleOwner),
			CreatedAt: row.CreatedAt.Time,
		}
		return recordAudit(ctx, q, orgID, userID, "organization.created", "organization", &orgID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListForUser returns the organizations the caller belongs to.
func (s *Service) ListForUser(ctx context.Context, userID uuid.UUID) ([]Organization, error) {
	out := []Organization{}
	err := s.pool.AsUser(ctx, userID, func(tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListOrganizationsForUser(ctx, userID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, r := range rows {
			role := Role(r.Role)
			out = append(out, Organization{
				ID: r.ID, Name: r.Name, Slug: r.Slug,
				Role: role, Permissions: PermissionsFor(role),
				CreatedAt: r.CreatedAt.Time,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Membership is the caller's standing in an organization, resolved before every action.
type Membership struct {
	OrgID uuid.UUID
	Name  string
	Slug  string
	Role  Role
}

// Resolve finds the caller's membership by organization slug. A slug the caller cannot see
// reports not found rather than forbidden, so the response cannot confirm it exists.
func (s *Service) Resolve(ctx context.Context, userID uuid.UUID, slug string) (*Membership, error) {
	var out *Membership
	err := s.pool.AsUser(ctx, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		orgRow, err := q.GetOrganizationBySlug(ctx, slug)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("organization").WithCause(err)
			}
			return httpx.Internal(err)
		}

		member, err := q.GetMembershipForUser(ctx, sqlc.GetMembershipForUserParams{
			OrgID: orgRow.ID, UserID: userID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("organization").WithCause(err)
			}
			return httpx.Internal(err)
		}

		out = &Membership{OrgID: orgRow.ID, Name: orgRow.Name, Slug: orgRow.Slug, Role: Role(member.Role)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Rename changes the display name, leaving the slug stable so links keep working.
func (s *Service) Rename(ctx context.Context, m *Membership, userID uuid.UUID, name string) (*Organization, error) {
	if err := m.Role.Require(CanManageOrganization); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if err := validateOrgName(name); err != nil {
		return nil, err
	}

	var out *Organization
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.RenameOrganization(ctx, sqlc.RenameOrganizationParams{ID: m.OrgID, Name: name})
		if err != nil {
			return httpx.Internal(err)
		}
		out = &Organization{
			ID: row.ID, Name: row.Name, Slug: row.Slug,
			Role: m.Role, Permissions: PermissionsFor(m.Role),
			CreatedAt: row.CreatedAt.Time,
		}
		return recordAudit(ctx, q, m.OrgID, userID, "organization.renamed", "organization", &m.OrgID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListMembers returns everyone in the organization.
func (s *Service) ListMembers(ctx context.Context, m *Membership, userID uuid.UUID) ([]Member, error) {
	out := []Member{}
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		rows, err := q.ListMembers(ctx, m.OrgID)
		if err != nil {
			return httpx.Internal(err)
		}
		owners, err := q.CountOwners(ctx, m.OrgID)
		if err != nil {
			return httpx.Internal(err)
		}

		canManage := m.Role.Can(CanManageMembers)
		for _, r := range rows {
			role := Role(r.Role)
			// The last owner cannot be removed, or the organization becomes unmanageable.
			removable := canManage && !(role == RoleOwner && owners <= 1)
			out = append(out, Member{
				UserID: r.UserID, Email: r.Email, Name: r.Name, Role: role,
				JoinedAt: r.CreatedAt.Time, CanBeRemove: removable,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Invite creates an invitation and returns it with the single-use token.
func (s *Service) Invite(ctx context.Context, m *Membership, userID uuid.UUID, email string, role Role) (*Invitation, string, error) {
	if err := m.Role.Require(CanManageMembers); err != nil {
		return nil, "", err
	}
	if err := role.Validate(); err != nil {
		return nil, "", err
	}
	// Only an owner may create another owner, so an admin cannot escalate past their role.
	if role == RoleOwner && m.Role != RoleOwner {
		return nil, "", httpx.NotAuthorized()
	}

	email = strings.ToLower(strings.TrimSpace(email))
	if err := validateInviteEmail(email); err != nil {
		return nil, "", err
	}

	token, hash, err := newInviteToken()
	if err != nil {
		return nil, "", httpx.Internal(err)
	}

	var out *Invitation
	err = s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.CreateInvitation(ctx, sqlc.CreateInvitationParams{
			ID:          uuid.New(),
			OrgID:       m.OrgID,
			Email:       email,
			Role:        sqlc.MembershipRole(role),
			TokenHash:   hash,
			InvitedByID: &userID,
			ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(InvitationTTL), Valid: true},
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.AlreadyExists("That person has already been invited.").
					WithField("email", "An invitation is already waiting for this address.")
			}
			return httpx.Internal(err)
		}
		out = &Invitation{
			ID: row.ID, Email: row.Email, Role: Role(row.Role),
			ExpiresAt: row.ExpiresAt.Time, CreatedAt: row.CreatedAt.Time,
		}
		return recordAudit(ctx, q, m.OrgID, userID, "invitation.created", "invitation", &row.ID)
	})
	if err != nil {
		return nil, "", err
	}
	return out, token, nil
}

// ListInvitations returns pending invitations.
func (s *Service) ListInvitations(ctx context.Context, m *Membership, userID uuid.UUID) ([]Invitation, error) {
	if err := m.Role.Require(CanManageMembers); err != nil {
		return nil, err
	}

	out := []Invitation{}
	err := s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListPendingInvitations(ctx, m.OrgID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, r := range rows {
			out = append(out, Invitation{
				ID: r.ID, Email: r.Email, Role: Role(r.Role),
				ExpiresAt: r.ExpiresAt.Time, CreatedAt: r.CreatedAt.Time,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Revoke cancels a pending invitation.
func (s *Service) Revoke(ctx context.Context, m *Membership, userID, invitationID uuid.UUID) error {
	if err := m.Role.Require(CanManageMembers); err != nil {
		return err
	}
	return s.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if err := q.DeleteInvitation(ctx, sqlc.DeleteInvitationParams{ID: invitationID, OrgID: m.OrgID}); err != nil {
			return httpx.Internal(err)
		}
		return recordAudit(ctx, q, m.OrgID, userID, "invitation.revoked", "invitation", &invitationID)
	})
}

// pendingInvite is the row returned by the token lookup function.
type pendingInvite struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Email     string
	Role      Role
	ExpiresAt time.Time
	OrgName   string
	OrgSlug   string
}

// findInvitation reads an invitation by token. It calls the dedicated database function
// because the invitee is not yet a member and the tenant policy would hide the row.
func (s *Service) findInvitation(ctx context.Context, tx pgx.Tx, token string) (*pendingInvite, error) {
	var p pendingInvite
	err := tx.QueryRow(ctx,
		`SELECT id, org_id, email, role, expires_at, org_name, org_slug
		 FROM find_invitation_by_token($1)`, hashInviteToken(token),
	).Scan(&p.ID, &p.OrgID, &p.Email, &p.Role, &p.ExpiresAt, &p.OrgName, &p.OrgSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFound("invitation").WithCause(err)
		}
		return nil, httpx.Internal(err)
	}
	return &p, nil
}

// PreviewInvitation describes an invitation before it is accepted. Callers may be signed
// out, so this reveals only the organization name and the invited address.
func (s *Service) PreviewInvitation(ctx context.Context, token, currentEmail string) (*InvitationPreview, error) {
	var out *InvitationPreview
	err := s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		p, err := s.findInvitation(ctx, tx, token)
		if err != nil {
			return err
		}
		out = &InvitationPreview{
			OrgName: p.OrgName, OrgSlug: p.OrgSlug, Email: p.Email, Role: p.Role,
			Matches:   currentEmail != "" && strings.EqualFold(currentEmail, p.Email),
			NeedsAuth: currentEmail == "",
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Accept joins the caller to the organization the invitation names.
func (s *Service) Accept(ctx context.Context, token string, userID uuid.UUID, userEmail string) (*Organization, error) {
	var invite *pendingInvite

	// Read the invitation first, since its organization is not known until then.
	err := s.pool.Unscoped(ctx, func(tx pgx.Tx) error {
		p, err := s.findInvitation(ctx, tx, token)
		if err != nil {
			return err
		}
		// An invitation is for one address, so a forwarded link cannot be used by someone else.
		if !strings.EqualFold(p.Email, userEmail) {
			return httpx.NotAuthorized().
				WithCause(fmt.Errorf("invitation for %s used by %s", p.Email, userEmail))
		}
		invite = p
		return nil
	})
	if err != nil {
		return nil, err
	}

	var out *Organization
	err = s.pool.InOrgAsUser(ctx, invite.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.CreateMembership(ctx, sqlc.CreateMembershipParams{
			ID: uuid.New(), OrgID: invite.OrgID, UserID: userID, Role: sqlc.MembershipRole(invite.Role),
		}); err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.AlreadyExists("You are already a member of this organization.")
			}
			return httpx.Internal(err)
		}
		if err := q.AcceptInvitation(ctx, invite.ID); err != nil {
			return httpx.Internal(err)
		}

		row, err := q.GetOrganizationByID(ctx, invite.OrgID)
		if err != nil {
			return httpx.Internal(err)
		}
		out = &Organization{
			ID: row.ID, Name: row.Name, Slug: row.Slug,
			Role: invite.Role, Permissions: PermissionsFor(invite.Role),
			CreatedAt: row.CreatedAt.Time,
		}
		return recordAudit(ctx, q, invite.OrgID, userID, "invitation.accepted", "invitation", &invite.ID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ChangeRole updates a member's role.
func (s *Service) ChangeRole(ctx context.Context, m *Membership, actorID, targetID uuid.UUID, role Role) (*Member, error) {
	if err := m.Role.Require(CanManageMembers); err != nil {
		return nil, err
	}
	if err := role.Validate(); err != nil {
		return nil, err
	}
	if role == RoleOwner && m.Role != RoleOwner {
		return nil, httpx.NotAuthorized()
	}

	var out *Member
	err := s.pool.InOrgAsUser(ctx, m.OrgID, actorID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if err := s.guardLastOwner(ctx, q, m.OrgID, targetID, role); err != nil {
			return err
		}

		row, err := q.UpdateMemberRole(ctx, sqlc.UpdateMemberRoleParams{
			OrgID: m.OrgID, UserID: targetID, Role: sqlc.MembershipRole(role),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFound("member").WithCause(err)
			}
			return httpx.Internal(err)
		}
		out = &Member{UserID: row.UserID, Role: Role(row.Role), JoinedAt: row.CreatedAt.Time}
		return recordAudit(ctx, q, m.OrgID, actorID, "member.role_changed", "user", &targetID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveMember takes someone out of the organization.
func (s *Service) RemoveMember(ctx context.Context, m *Membership, actorID, targetID uuid.UUID) error {
	if err := m.Role.Require(CanManageMembers); err != nil {
		return err
	}
	return s.pool.InOrgAsUser(ctx, m.OrgID, actorID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if err := s.guardLastOwner(ctx, q, m.OrgID, targetID, ""); err != nil {
			return err
		}
		if err := q.DeleteMembership(ctx, sqlc.DeleteMembershipParams{OrgID: m.OrgID, UserID: targetID}); err != nil {
			return httpx.Internal(err)
		}
		return recordAudit(ctx, q, m.OrgID, actorID, "member.removed", "user", &targetID)
	})
}

// guardLastOwner refuses changes that would leave an organization with no owner, which
// would make it permanently unmanageable. An empty newRole means removal.
func (s *Service) guardLastOwner(ctx context.Context, q *sqlc.Queries, orgID, targetID uuid.UUID, newRole Role) error {
	current, err := q.GetMembershipForUser(ctx, sqlc.GetMembershipForUserParams{OrgID: orgID, UserID: targetID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("member").WithCause(err)
		}
		return httpx.Internal(err)
	}
	if Role(current.Role) != RoleOwner || newRole == RoleOwner {
		return nil
	}

	owners, err := q.CountOwners(ctx, orgID)
	if err != nil {
		return httpx.Internal(err)
	}
	if owners <= 1 {
		return httpx.Conflict("This is the only owner. Make someone else an owner first.")
	}
	return nil
}

func recordAudit(ctx context.Context, q *sqlc.Queries, orgID, actorID uuid.UUID, action, targetType string, targetID *uuid.UUID) error {
	err := q.RecordAuditEvent(ctx, sqlc.RecordAuditEventParams{
		ID:          uuid.New(),
		OrgID:       &orgID,
		ActorUserID: &actorID,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Metadata:    []byte(`{}`),
	})
	if err != nil {
		return httpx.Internal(err)
	}
	return nil
}

func validateOrgName(name string) *httpx.Error {
	if name == "" {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("name", "Please enter a name for your organization.")
	}
	if utf8.RuneCountInString(name) > maxOrgNameLength {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("name", "Please use a shorter name.")
	}
	return nil
}

func validateInviteEmail(email string) *httpx.Error {
	if email == "" || !strings.Contains(email, "@") {
		return httpx.InvalidInput("Please check the highlighted fields and try again.").
			WithField("email", "Please enter a valid email address.")
	}
	return nil
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// slugify builds a stable URL name, suffixed so two organizations may share a display name.
func slugify(name string, id uuid.UUID) string {
	base := strings.Trim(nonSlugChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "org"
	}
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	return base + "-" + id.String()[:6]
}
