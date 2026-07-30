package deploy

import (
	"context"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/ebnsina/yol/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Variables an app runs with are usually the most sensitive thing a customer gives us: database
// passwords, signing keys, third-party credentials. They are encrypted before they are stored, and
// they are never sent back. A client can list what is set and change a value, and that is all.

// Variable is what a client is shown: the name and when it changed, never the value.
type Variable struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListVariables returns the names set for an environment.
func (p *Projects) ListVariables(ctx context.Context, m *org.Membership, userID, envID uuid.UUID) ([]Variable, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := []Variable{}
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetEnvironment(ctx, envID); err != nil {
			return notFoundOr(err, "environment")
		}

		rows, err := q.ListEnvVarNames(ctx, envID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out = append(out, Variable{Name: row.Name, UpdatedAt: row.UpdatedAt.Time})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetVariable stores a value, replacing whatever was there. The next deploy is what carries it to
// the server, since changing what a running container was started with is not possible.
func (p *Projects) SetVariable(
	ctx context.Context,
	m *org.Membership,
	userID, envID uuid.UUID,
	name, value string,
) error {
	if err := m.Role.Require(org.CanDeploy); err != nil {
		return err
	}
	if err := validateVariableName(name); err != nil {
		return err
	}

	return p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetEnvironment(ctx, envID); err != nil {
			return notFoundOr(err, "environment")
		}

		// Bound to what it is for, so a value stored as a variable cannot be read back through a
		// path meant for something else.
		sealed := p.secrets.SealString(value, secrets.ContextEnvVar)

		if err := q.SetEnvVar(ctx, sqlc.SetEnvVarParams{
			ID:    uuid.New(),
			OrgID: m.OrgID,
			EnvID: envID,
			Name:  name,
			Value: sealed,
		}); err != nil {
			return httpx.Internal(err)
		}
		return nil
	})
}

// DeleteVariable removes one.
func (p *Projects) DeleteVariable(ctx context.Context, m *org.Membership, userID, envID uuid.UUID, name string) error {
	if err := m.Role.Require(org.CanDeploy); err != nil {
		return err
	}

	return p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetEnvironment(ctx, envID); err != nil {
			return notFoundOr(err, "environment")
		}
		if err := q.DeleteEnvVar(ctx, sqlc.DeleteEnvVarParams{EnvID: envID, Name: name}); err != nil {
			return httpx.Internal(err)
		}
		return nil
	})
}

// variablesFor reads an environment's variables back for a deploy. Only ever called to hand them to
// the server that runs the app; nothing reachable by a client returns these.
func (p *Projects) variablesFor(ctx context.Context, q *sqlc.Queries, envID uuid.UUID) (map[string]string, error) {
	rows, err := q.ListEnvVars(ctx, envID)
	if err != nil {
		return nil, httpx.Internal(err)
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		value, err := p.secrets.OpenString(row.Value, secrets.ContextEnvVar)
		if err != nil {
			// The key changed or the value was tampered with. Deploying with a variable silently
			// missing would be worse than not deploying at all.
			return nil, httpx.Internal(err)
		}
		out[row.Name] = value
	}
	return out, nil
}

// validateVariableName keeps names to what a shell and a container will actually carry, so a name
// that cannot work is refused when it is set rather than at deploy time.
func validateVariableName(name string) error {
	if name == "" {
		return httpx.InvalidInput("Please give this variable a name.").
			WithField("name", "A name is needed.")
	}
	if len(name) > 128 {
		return httpx.InvalidInput("That name is too long.").
			WithField("name", "Use 128 characters or fewer.")
	}
	if name[0] >= '0' && name[0] <= '9' {
		return httpx.InvalidInput("A variable name cannot start with a number.").
			WithField("name", "Start with a letter or an underscore.")
	}
	for i := range len(name) {
		c := name[i]
		letter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
		if !letter && !(c >= '0' && c <= '9') && c != '_' {
			return httpx.InvalidInput("A variable name uses letters, numbers and underscores only.").
				WithField("name", "Remove "+strings.TrimSpace(string(c))+" from the name.")
		}
	}
	return nil
}
