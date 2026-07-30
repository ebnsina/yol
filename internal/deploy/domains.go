package deploy

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/ebnsina/yol/internal/db"
	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Until somebody adds a hostname, an app is reached by the address of the server it was placed on,
// over plain HTTP. No certificate authority issues for an address, so a certificate there is not
// something we can obtain rather than something we chose not to. Adding a hostname is what turns
// HTTPS on, and it needs nothing bought from us: the name is theirs and the server is theirs.
//
// A hostname is only served once it points at that server. Otherwise a certificate would be
// requested for a name somebody else controls, which is the exact thing the router asks us about.

// Address is where an environment can be reached, and what still stands between it and HTTPS.
type Address struct {
	// The plain address of the server, present while there is one to give.
	URL string `json:"url"`
	// Every hostname added, verified or not.
	Domains []Domain `json:"domains"`
	// True when nothing but an address is available, so the interface can say why.
	AddressOnly bool `json:"addressOnly"`
}

// Domain is one hostname an app answers for.
type Domain struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	// True for a subdomain we hand out, which needs no verifying because we own the parent.
	Ours       bool       `json:"ours"`
	Verified   bool       `json:"verified"`
	AddedAt    time.Time  `json:"addedAt"`
	VerifiedAt *time.Time `json:"verifiedAt"`
	// What to create in DNS, present until it is verified.
	Record *DNSRecord `json:"record"`
}

// DNSRecord is the record somebody has to create, spelled out rather than described.
type DNSRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ErrNotPointedHere means the hostname does not resolve to the server it was added to.
var ErrNotPointedHere = errors.New("deploy: the hostname does not point at this server")

// resolveTimeout bounds a lookup. A name that was only just created often does not resolve at all,
// and waiting on that is what makes an interface feel stuck.
const resolveTimeout = 5 * time.Second

// AddressFor describes how an environment is reached today.
func (p *Projects) AddressFor(
	ctx context.Context,
	m *org.Membership,
	userID, envID uuid.UUID,
) (*Address, error) {
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		return nil, err
	}

	out := &Address{Domains: []Domain{}}
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		target, err := q.GetDeployTarget(ctx, envID)
		if err != nil {
			return notFoundOr(err, "environment")
		}

		host := ""
		if target.ServerID != nil {
			server, err := q.GetServer(ctx, *target.ServerID)
			if err != nil {
				return notFoundOr(err, "server")
			}
			host = server.Host
			out.URL = "http://" + server.Host
		}

		rows, err := q.ListDomains(ctx, target.ServiceID)
		if err != nil {
			return httpx.Internal(err)
		}
		for _, row := range rows {
			out.Domains = append(out.Domains, toDomain(row, host))
		}

		// Only a verified hostname is actually being served, so an unverified one does not count
		// as somewhere the app can be reached.
		for _, domain := range out.Domains {
			if domain.Verified || domain.Ours {
				return nil
			}
		}
		out.AddressOnly = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AddDomain records a hostname for an environment's app and says what to put in DNS.
//
// Nothing is served yet: the hostname has to be shown to point here first. That check is what stops
// somebody adding a name they do not control and having a certificate requested for it.
func (p *Projects) AddDomain(
	ctx context.Context,
	m *org.Membership,
	userID, envID uuid.UUID,
	hostname string,
) (*Domain, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}

	hostname, err := cleanHostname(hostname)
	if err != nil {
		return nil, err
	}

	var out *Domain
	err = p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		target, err := q.GetDeployTarget(ctx, envID)
		if err != nil {
			return notFoundOr(err, "environment")
		}
		if target.ServerID == nil {
			return httpx.InvalidInput("Give this environment a server first, so there is somewhere to point the hostname.").
				WithField("serverId", "None is chosen yet.")
		}

		server, err := q.GetServer(ctx, *target.ServerID)
		if err != nil {
			return notFoundOr(err, "server")
		}

		row, err := q.CreateDomain(ctx, sqlc.CreateDomainParams{
			ID:        uuid.New(),
			OrgID:     m.OrgID,
			ServiceID: target.ServiceID,
			Hostname:  hostname,
			// Somebody else's name, so it is verified before anything is served for it.
			Ours:       false,
			VerifiedAt: pgtype.Timestamptz{},
		})
		if err != nil {
			if db.IsUniqueViolation(err) {
				return httpx.AlreadyExists("That hostname is already in use.").
					WithField("hostname", "This name is already pointed at an app.")
			}
			return httpx.Internal(err)
		}

		shown := toDomain(row, server.Host)
		out = &shown
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// VerifyDomain checks that a hostname resolves to the server it was added to, and starts serving it
// when it does. Asked for rather than polled, because DNS takes as long as it takes and somebody
// pressing a button knows they have made the change.
func (p *Projects) VerifyDomain(
	ctx context.Context,
	m *org.Membership,
	userID, domainID uuid.UUID,
) (*Domain, error) {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return nil, err
	}

	var (
		hostname string
		host     string
		serverID *uuid.UUID
	)
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		found, err := q.GetDomain(ctx, domainID)
		if err != nil {
			return notFoundOr(err, "domain")
		}
		hostname = found.Hostname
		if found.ServerID == nil {
			return httpx.InvalidInput("This environment has no server, so there is nothing for a hostname to point at.")
		}

		server, err := q.GetServer(ctx, *found.ServerID)
		if err != nil {
			return notFoundOr(err, "server")
		}
		host = server.Host
		serverID = found.ServerID
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := pointsAt(ctx, hostname, host); err != nil {
		if errors.Is(err, ErrNotPointedHere) {
			return nil, httpx.InvalidInput(
				"That hostname does not point here yet. DNS changes can take a while to spread.").
				WithField("hostname", "Create the record shown, then try again.").WithCause(err)
		}
		return nil, httpx.InvalidInput("We could not look that hostname up.").WithCause(err)
	}

	var out *Domain
	err = p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		if err := q.MarkDomainVerified(ctx, sqlc.MarkDomainVerifiedParams{
			ID:    domainID,
			OrgID: m.OrgID,
		}); err != nil {
			return httpx.Internal(err)
		}

		found, err := q.GetDomain(ctx, domainID)
		if err != nil {
			return notFoundOr(err, "domain")
		}
		shown := Domain{
			ID:       found.ID.String(),
			Hostname: found.Hostname,
			Ours:     found.Ours,
			Verified: found.VerifiedAt.Valid,
			AddedAt:  found.CreatedAt.Time,
		}
		if found.VerifiedAt.Valid {
			at := found.VerifiedAt.Time
			shown.VerifiedAt = &at
		}
		out = &shown
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The server is handed its desired state, which is what makes the router answer for the
	// hostname and obtain a certificate for it.
	if p.agents != nil && serverID != nil {
		if err := p.agents.Reconcile(ctx, *serverID); err != nil {
			return nil, httpx.Internal(err)
		}
	}
	return out, nil
}

// RemoveDomain stops serving a hostname.
func (p *Projects) RemoveDomain(ctx context.Context, m *org.Membership, userID, domainID uuid.UUID) error {
	if err := m.Role.Require(org.CanManageServers); err != nil {
		return err
	}

	var serverID *uuid.UUID
	err := p.pool.InOrgAsUser(ctx, m.OrgID, userID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		found, err := q.GetDomain(ctx, domainID)
		if err != nil {
			return notFoundOr(err, "domain")
		}
		serverID = found.ServerID

		if err := q.DeleteDomain(ctx, sqlc.DeleteDomainParams{ID: domainID, OrgID: m.OrgID}); err != nil {
			return httpx.Internal(err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if p.agents != nil && serverID != nil {
		if err := p.agents.Reconcile(ctx, *serverID); err != nil {
			return httpx.Internal(err)
		}
	}
	return nil
}

// pointsAt reports whether a hostname resolves to the server's address.
//
// Compared by address rather than by asking for a particular kind of record, because somebody may
// point a name here with an A record, an AAAA record, or a name that leads to one.
func pointsAt(ctx context.Context, hostname, host string) error {
	lookupCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	var resolver net.Resolver
	found, err := resolver.LookupHost(lookupCtx, hostname)
	if err != nil {
		return err
	}

	// The server may be recorded by address or by name, so what it resolves to is what counts.
	wanted := map[string]bool{}
	if net.ParseIP(host) != nil {
		wanted[host] = true
	} else {
		addresses, err := resolver.LookupHost(lookupCtx, host)
		if err != nil {
			return err
		}
		for _, address := range addresses {
			wanted[address] = true
		}
	}

	for _, address := range found {
		if wanted[address] {
			return nil
		}
	}
	return ErrNotPointedHere
}

// cleanHostname accepts what somebody is likely to paste and refuses what cannot work.
func cleanHostname(hostname string) (string, error) {
	hostname = strings.TrimSpace(strings.ToLower(hostname))
	hostname = strings.TrimPrefix(strings.TrimPrefix(hostname, "https://"), "http://")
	hostname = strings.TrimSuffix(strings.SplitN(hostname, "/", 2)[0], ".")

	if hostname == "" {
		return "", httpx.InvalidInput("Please enter a hostname.").
			WithField("hostname", "A hostname is needed.")
	}
	if net.ParseIP(hostname) != nil {
		return "", httpx.InvalidInput("An app is already reachable by its server's address.").
			WithField("hostname", "Enter a hostname such as app.example.com.")
	}
	if !strings.Contains(hostname, ".") || strings.Contains(hostname, " ") {
		return "", httpx.InvalidInput("That does not look like a hostname.").
			WithField("hostname", "Enter something like app.example.com.")
	}
	if len(hostname) > 253 {
		return "", httpx.InvalidInput("That hostname is too long.").
			WithField("hostname", "Hostnames are at most 253 characters.")
	}
	return hostname, nil
}

func toDomain(row sqlc.Domain, serverHost string) Domain {
	domain := Domain{
		ID:       row.ID.String(),
		Hostname: row.Hostname,
		Ours:     row.Ours,
		Verified: row.VerifiedAt.Valid,
		AddedAt:  row.CreatedAt.Time,
	}
	if row.VerifiedAt.Valid {
		at := row.VerifiedAt.Time
		domain.VerifiedAt = &at
	}

	// Spelled out while it is still needed, so nobody has to work out what to create.
	if !row.VerifiedAt.Valid && !row.Ours && serverHost != "" {
		record := DNSRecord{Type: "A", Name: row.Hostname, Value: serverHost}
		if net.ParseIP(serverHost) == nil {
			record.Type = "CNAME"
		}
		domain.Record = &record
	}
	return domain
}
