package deploy

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/github"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Connecting a repository is two steps that are easy to conflate. Installing our application on
// GitHub is what grants access, and it is done on GitHub and confirmed with GitHub rather than
// taken on trust from whatever the browser came back with. Choosing which repository a project
// builds from is a separate, reversible decision made here.

// Code is what a project builds from, and where the installation it needs came from.
type Code interface {
	// Installation reads one from GitHub, so a callback is confirmed rather than believed.
	Installation(ctx context.Context, installationID int64) (*github.Installation, error)
	// Repositories lists what an installation gives access to.
	Repositories(ctx context.Context, installationID int64) ([]github.Repository, error)
	// InstallURL is where somebody is sent to grant access.
	InstallURL() string
	// InstallationToken mints a credential for one installation, lasting an hour.
	InstallationToken(ctx context.Context, installationID int64) (string, error)
}

// Installation is what a client is shown about access we were granted.
type Installation struct {
	ID        string    `json:"id"`
	Account   string    `json:"account"`
	CreatedAt time.Time `json:"createdAt"`
}

// SetCode gives the service somewhere to reach repositories.
func (p *Projects) SetCode(code Code) { p.code = code }

// InstallURL is where somebody goes to grant access to their repositories.
func (p *Projects) InstallURL() string {
	if p.code == nil {
		return ""
	}
	return p.code.InstallURL()
}

// ConnectInstallation records access somebody granted on GitHub.
//
// What the browser comes back with is only an identifier, and anybody could put any number there.
// So it is read from GitHub first: if it does not exist, it is not recorded.
func (p *Projects) ConnectInstallation(
	ctx context.Context,
	m *org.Membership,
	userID uuid.UUID,
	installationID int64,
) (*Installation, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}
	if p.code == nil {
		return nil, httpx.Internal(errNoCode)
	}

	found, err := p.code.Installation(ctx, installationID)
	if err != nil {
		return nil, httpx.InvalidInput("We could not confirm that installation with GitHub.").
			WithCause(err)
	}

	var out *Installation
	err = p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		row, err := sqlc.New(tx).CreateInstallation(ctx, sqlc.CreateInstallationParams{
			ID:          uuid.New(),
			OrgID:       m.OrgID,
			ExternalID:  strconv.FormatInt(found.ID, 10),
			Account:     found.Account,
			ConnectedBy: &userID,
		})
		if err != nil {
			return httpx.Internal(err)
		}
		out = &Installation{
			ID:        row.ExternalID,
			Account:   row.Account,
			CreatedAt: row.CreatedAt.Time,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListInstallations returns the access this organization has been granted.
func (p *Projects) ListInstallations(ctx context.Context, m *org.Membership, userID uuid.UUID) ([]Installation, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []Installation{}
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListInstallations(ctx, m.OrgID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, Installation{
				ID:        row.ExternalID,
				Account:   row.Account,
				CreatedAt: row.CreatedAt.Time,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListRepositories returns what an installation gives access to, for choosing from.
//
// The installation is checked against this organization's own records first, so somebody cannot
// list another organization's repositories by naming their installation.
func (p *Projects) ListRepositories(
	ctx context.Context,
	m *org.Membership,
	userID uuid.UUID,
	installationID string,
) ([]github.Repository, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}
	if p.code == nil {
		return nil, httpx.Internal(errNoCode)
	}

	if err := p.ownsInstallation(ctx, m.OrgID, userID, installationID); err != nil {
		return nil, err
	}

	numeric, err := strconv.ParseInt(installationID, 10, 64)
	if err != nil {
		return nil, httpx.NotFound("installation").WithCause(err)
	}

	repositories, err := p.code.Repositories(ctx, numeric)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return repositories, nil
}

// ConnectRepository is what a project builds from.
type ConnectRepository struct {
	InstallationID string
	RepositoryID   string
	FullName       string
}

// ConnectRepositoryToProject records where a project's code comes from. Reversible: pointing a
// project at a different repository is the same call again.
func (p *Projects) ConnectRepositoryToProject(
	ctx context.Context,
	m *org.Membership,
	userID, projectID uuid.UUID,
	in ConnectRepository,
) (*Project, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.FullName) == "" || strings.TrimSpace(in.RepositoryID) == "" {
		return nil, httpx.InvalidInput("Please choose a repository.").
			WithField("repositoryId", "A repository is needed.")
	}
	if err := p.ownsInstallation(ctx, m.OrgID, userID, in.InstallationID); err != nil {
		return nil, err
	}

	var out *Project
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetProject(ctx, projectID); err != nil {
			return notFoundOr(err, "project")
		}

		provider := github.Provider
		row, err := q.SetProjectRepository(ctx, sqlc.SetProjectRepositoryParams{
			ID:                 projectID,
			RepoProvider:       &provider,
			RepoFullName:       &in.FullName,
			RepoExternalID:     &in.RepositoryID,
			RepoInstallationID: &in.InstallationID,
		})
		if err != nil {
			return httpx.Internal(err)
		}
		out = toProject(row, m.Role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ownsInstallation reports whether this organization holds the installation. A missing one is
// reported as not found, so naming another organization's installation cannot be used to discover
// that it exists.
func (p *Projects) ownsInstallation(ctx context.Context, orgID, userID uuid.UUID, installationID string) error {
	if installationID == "" {
		return httpx.InvalidInput("Please connect your code first.").
			WithField("installationId", "This is needed.")
	}

	return p.pool.InOrgAsUser(ctx, orgID, userID, func(tx pgx.Tx) error {
		_, err := sqlc.New(tx).GetInstallation(ctx, sqlc.GetInstallationParams{
			OrgID:      orgID,
			ExternalID: installationID,
		})
		if err != nil {
			return notFoundOr(err, "installation")
		}
		return nil
	})
}
