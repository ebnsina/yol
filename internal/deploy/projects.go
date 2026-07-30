package deploy

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/ebnsina/yol/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A project holds the environments an app is deployed into. Two are created with it, because
// wanting somewhere to try a change before it reaches the public is the normal case rather than an
// advanced one, and creating the second later means moving settings across by hand.

const (
	productionName = "production"
	stagingName    = "staging"

	defaultProductionBranch = "main"
	defaultStagingBranch    = "develop"

	// Enough for a small app and its dependencies, and small enough that several fit on a modest
	// server. Changed per service by whoever knows what their app needs.
	defaultServiceMemory = 512 << 20
)

// Projects owns projects, their environments, and the services inside them.
type Projects struct {
	pool    *db.Pool
	secrets *secrets.Box
}

// NewProjects builds the service.
func NewProjects(pool *db.Pool, box *secrets.Box) *Projects {
	return &Projects{pool: pool, secrets: box}
}

// Project is what clients are shown.
type Project struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Slug         string        `json:"slug"`
	Repository   *Repository   `json:"repository"`
	Environments []Environment `json:"environments,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
	Permissions  Permissions   `json:"permissions"`
}

// Repository is where a project's code comes from, absent until one is connected.
type Repository struct {
	Provider string `json:"provider"`
	FullName string `json:"fullName"`
}

// Permissions tells the client what the caller may do here, so it renders from this rather than
// working the rules out from a role.
type Permissions struct {
	Deploy bool `json:"deploy"`
	Manage bool `json:"manage"`
}

// Environment is one place a project is deployed to.
type Environment struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Branch   string    `json:"branch"`
	ServerID *string   `json:"serverId"`
	Services []Service `json:"services,omitempty"`
}

// Service is one thing running in an environment.
type Service struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`
	HealthPath       *string `json:"healthPath"`
	HealthPort       *int    `json:"healthPort"`
	MemoryLimitBytes int64   `json:"memoryLimitBytes"`
}

// Create records a project with a production and a staging environment, each holding one app.
func (p *Projects) Create(ctx context.Context, m *org.Membership, userID uuid.UUID, name string) (*Project, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, httpx.InvalidInput("Please give this project a name.").
			WithField("name", "A name is needed.")
	}

	projectID := uuid.New()
	var out *Project

	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		project, err := q.CreateProject(ctx, sqlc.CreateProjectParams{
			ID:    projectID,
			OrgID: m.OrgID,
			Name:  name,
			Slug:  slugify(name, projectID),
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.AlreadyExists("A project with that name already exists here.").
					WithField("name", "This name is already in use.")
			}
			return httpx.Internal(err)
		}

		out = toProject(project, m.Role)
		for _, wanted := range []struct{ name, branch string }{
			{productionName, defaultProductionBranch},
			{stagingName, defaultStagingBranch},
		} {
			environment, err := p.createEnvironment(ctx, q, m.OrgID, projectID, wanted.name, wanted.branch, project.Slug)
			if err != nil {
				return err
			}
			out.Environments = append(out.Environments, *environment)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// createEnvironment adds an environment and the app inside it.
func (p *Projects) createEnvironment(
	ctx context.Context,
	q *sqlc.Queries,
	orgID, projectID uuid.UUID,
	name, branch, appName string,
) (*Environment, error) {
	envID := uuid.New()
	row, err := q.CreateEnvironment(ctx, sqlc.CreateEnvironmentParams{
		ID:        envID,
		OrgID:     orgID,
		ProjectID: projectID,
		Name:      name,
		Branch:    branch,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	service, err := q.CreateService(ctx, sqlc.CreateServiceParams{
		ID:               uuid.New(),
		OrgID:            orgID,
		EnvID:            envID,
		Name:             appName,
		Kind:             sqlc.ServiceKindApp,
		MemoryLimitBytes: defaultServiceMemory,
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	environment := toEnvironment(row)
	environment.Services = []Service{toService(service)}
	return &environment, nil
}

// List returns the organization's projects, without their contents.
func (p *Projects) List(ctx context.Context, m *org.Membership, userID uuid.UUID) ([]Project, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []Project{}
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		rows, err := sqlc.New(tx).ListProjects(ctx, m.OrgID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, *toProject(row, m.Role))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one project with its environments and their services.
func (p *Projects) Get(ctx context.Context, m *org.Membership, userID, projectID uuid.UUID) (*Project, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	var out *Project
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetProject(ctx, projectID)
		if err != nil {
			return notFoundOr(err, "project")
		}
		out = toProject(row, m.Role)

		environments, err := q.ListEnvironments(ctx, projectID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, environment := range environments {
			shown := toEnvironment(environment)

			services, err := q.ListServices(ctx, environment.ID)
			if err != nil {
				return httpx.Internal(err)
			}
			for _, service := range services {
				shown.Services = append(shown.Services, toService(service))
			}
			out.Environments = append(out.Environments, shown)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes a project and everything inside it. What is running on the servers goes away on
// the next pass, since the desired state no longer mentions it.
func (p *Projects) Delete(ctx context.Context, m *org.Membership, userID, projectID uuid.UUID) error {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return err
	}

	return p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetProject(ctx, projectID); err != nil {
			return notFoundOr(err, "project")
		}
		if err := q.DeleteProject(ctx, sqlc.DeleteProjectParams{ID: projectID, OrgID: m.OrgID}); err != nil {
			return httpx.Internal(err)
		}
		return nil
	})
}

// EnvironmentChanges is what may be changed about an environment. Both are optional, so a client
// can change one without having to send the other back unchanged.
type EnvironmentChanges struct {
	Branch   *string
	ServerID *uuid.UUID
}

// UpdateEnvironment changes which branch an environment follows and which server it runs on.
func (p *Projects) UpdateEnvironment(
	ctx context.Context,
	m *org.Membership,
	userID, envID uuid.UUID,
	changes EnvironmentChanges,
) (*Environment, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}

	var out *Environment
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetEnvironment(ctx, envID)
		if err != nil {
			return notFoundOr(err, "environment")
		}

		if changes.Branch != nil {
			branch := strings.TrimSpace(*changes.Branch)
			if branch == "" {
				return httpx.InvalidInput("Please say which branch this environment follows.").
					WithField("branch", "A branch is needed.")
			}
			if err := q.SetEnvironmentBranch(ctx, sqlc.SetEnvironmentBranchParams{
				ID:     envID,
				Branch: branch,
			}); err != nil {
				return httpx.Internal(err)
			}
			row.Branch = branch
		}

		if changes.ServerID != nil {
			// Checked here so assigning a server that is not this organization's reports that it
			// was not found, rather than failing later when a deploy has nowhere to go.
			if _, err := q.GetServer(ctx, *changes.ServerID); err != nil {
				return notFoundOr(err, "server")
			}
			if err := q.SetEnvironmentServer(ctx, sqlc.SetEnvironmentServerParams{
				ID:       envID,
				ServerID: changes.ServerID,
			}); err != nil {
				return httpx.Internal(err)
			}
			row.ServerID = changes.ServerID
		}

		shown := toEnvironment(row)
		out = &shown
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ServiceChanges is what may be changed about a service.
type ServiceChanges struct {
	HealthPath       *string
	HealthPort       *int
	MemoryLimitBytes *int64
}

// UpdateService changes how a service is checked and what it may consume.
func (p *Projects) UpdateService(
	ctx context.Context,
	m *org.Membership,
	userID, serviceID uuid.UUID,
	changes ServiceChanges,
) (*Service, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}
	if changes.HealthPath != nil && *changes.HealthPath != "" && !strings.HasPrefix(*changes.HealthPath, "/") {
		return nil, httpx.InvalidInput("A health check path starts with a slash.").
			WithField("healthPath", "This should look like /healthz.")
	}
	if changes.HealthPort != nil && (*changes.HealthPort < 1 || *changes.HealthPort > 65535) {
		return nil, httpx.InvalidInput("That is not a port an app can listen on.").
			WithField("healthPort", "Choose a port between 1 and 65535.")
	}
	if changes.MemoryLimitBytes != nil && *changes.MemoryLimitBytes < minServiceMemory {
		return nil, httpx.InvalidInput("That memory limit is too small for anything to run in.").
			WithField("memoryLimitBytes", "Allow at least 64MB.")
	}

	var out *Service
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetService(ctx, serviceID)
		if err != nil {
			return notFoundOr(err, "service")
		}

		if changes.HealthPath != nil {
			row.HealthPath = changes.HealthPath
		}
		if changes.HealthPort != nil {
			port := int32(*changes.HealthPort)
			row.HealthPort = &port
		}
		if changes.MemoryLimitBytes != nil {
			row.MemoryLimitBytes = *changes.MemoryLimitBytes
		}

		updated, err := q.UpdateService(ctx, sqlc.UpdateServiceParams{
			ID:               serviceID,
			HealthPath:       row.HealthPath,
			HealthPort:       row.HealthPort,
			MemoryLimitBytes: row.MemoryLimitBytes,
		})
		if err != nil {
			return httpx.Internal(err)
		}

		shown := toService(updated)
		out = &shown
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// minServiceMemory is the smallest limit worth accepting. Below this nothing starts, and a limit
// that stops an app from ever running is a worse outcome than refusing the setting.
const minServiceMemory = 64 << 20

func toProject(row sqlc.Project, role org.Role) *Project {
	project := &Project{
		ID:        row.ID.String(),
		Name:      row.Name,
		Slug:      row.Slug,
		CreatedAt: row.CreatedAt.Time,
		Permissions: Permissions{
			Deploy: role.Can(org.CanDeploy),
			Manage: role.Can(org.CanManageServers),
		},
	}
	if row.RepoProvider != nil && row.RepoFullName != nil {
		project.Repository = &Repository{Provider: *row.RepoProvider, FullName: *row.RepoFullName}
	}
	return project
}

func toEnvironment(row sqlc.Environment) Environment {
	environment := Environment{
		ID:     row.ID.String(),
		Name:   row.Name,
		Branch: row.Branch,
	}
	if row.ServerID != nil {
		id := row.ServerID.String()
		environment.ServerID = &id
	}
	return environment
}

func toService(row sqlc.Service) Service {
	service := Service{
		ID:               row.ID.String(),
		Name:             row.Name,
		Kind:             string(row.Kind),
		HealthPath:       row.HealthPath,
		MemoryLimitBytes: row.MemoryLimitBytes,
	}
	if row.HealthPort != nil {
		port := int(*row.HealthPort)
		service.HealthPort = &port
	}
	return service
}

// notFoundOr reports something as missing rather than as forbidden, so a response cannot be used
// to discover what exists in another organization. Row level security is what makes the two
// indistinguishable here: another organization's row simply is not visible.
func notFoundOr(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound(what).WithCause(err)
	}
	return httpx.Internal(err)
}

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// slugify builds a stable name for addresses, suffixed so two projects may share a display name.
func slugify(name string, id uuid.UUID) string {
	base := strings.Trim(nonSlugChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "project"
	}
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	return base + "-" + id.String()[:6]
}
