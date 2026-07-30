package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The router that serves web traffic. It is the first thing this platform runs on a customer's
// server, and its ports depend on the answer they gave about ports 80 and 443.
const (
	routerContainer = "yol-router"
	routerImage     = "caddy:2-alpine"
	routerMemory    = 128 << 20
)

// SpecFor builds the desired state of one server.
//
// Only what this platform owns appears here. Anything the customer put on the machine is
// deliberately absent, which is what keeps it out of reach of the agent's removal step.
func (s *Service) SpecFor(ctx context.Context, identity AgentIdentity) (*proto.Spec, error) {
	spec := &proto.Spec{
		ServerID: identity.ServerID.String(),
		IssuedAt: time.Now().UTC(),
	}

	err := s.pool.InOrg(ctx, identity.OrgID, func(tx pgx.Tx) error {
		q := sqlc.New(tx)
		row, err := q.GetServer(ctx, identity.ServerID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("server: no longer connected")
			}
			return err
		}

		// A watched server is sent an empty specification, so even a bug that reached this
		// point would ask for nothing.
		if row.Mode == sqlc.ServerModeWatch {
			return nil
		}

		if router := routerFor(row, identity.OrgID); router != nil {
			spec.Containers = append(spec.Containers, *router)
			spec.Volumes = append(spec.Volumes, proto.SpecVolume{
				Name:   routerContainer + "-data",
				Labels: proto.ManagedLabels(identity.OrgID.String(), "", "", "", "", proto.RoleRouter),
			})
		}

		// Containers the user asked us to manage that were already on the machine. They carry no
		// label of ours, so the agent recognises them from this list instead.
		adopted, err := q.ListDiscoveredResources(ctx, identity.ServerID)
		if err != nil {
			return err
		}
		for _, resource := range adopted {
			if resource.Kind != sqlc.DiscoveredKindContainer || !resource.AdoptedAt.Valid {
				continue
			}
			entry := proto.AdoptedContainer{Name: resource.ExternalID}
			if resource.AdoptedContainerCreatedAt.Valid {
				entry.CreatedAt = resource.AdoptedContainerCreatedAt.Time
			}
			spec.Adopted = append(spec.Adopted, entry)
		}

		// The version is the moment it was built, so an agent can tell a newer one from an older
		// one without the control plane keeping a counter.
		spec.Version = time.Now().UTC().UnixMilli()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return spec, nil
}

// routerFor describes the router, or nothing when this server does not need one.
func routerFor(row sqlc.Server, orgID uuid.UUID) *proto.SpecContainer {
	// Behind their own web server we would need ports allocated for it, which comes with
	// deployments. Until then only a server where we handle web traffic gets a router.
	if row.RoutingMode == nil || *row.RoutingMode != sqlc.RoutingModeTakeover {
		return nil
	}

	return &proto.SpecContainer{
		Name:   routerContainer,
		Image:  routerImage,
		Labels: proto.ManagedLabels(orgID.String(), "", "", "", "", proto.RoleRouter),
		Ports: []proto.PortMapping{
			{HostPort: 80, ContainerPort: 80, Protocol: "tcp"},
			{HostPort: 443, ContainerPort: 443, Protocol: "tcp"},
		},
		Mounts: []proto.Mount{
			{Source: routerContainer + "-data", Target: "/data"},
		},
		MemoryLimitBytes: routerMemory,
		RestartPolicy:    "unless-stopped",
	}
}

// SendSpec builds, signs and sends the desired state to a connected agent.
func (s *Service) SendSpec(ctx context.Context, conn *Connection, signer *proto.SigningKey) error {
	spec, err := s.SpecFor(ctx, conn.Identity)
	if err != nil {
		return err
	}

	signed, err := signer.Sign(*spec)
	if err != nil {
		return err
	}
	return conn.Send(ctx, proto.TypeApplySpec, signed)
}

// RecordApplied notes what an agent did, so a failure is visible rather than silent.
func (s *Service) RecordApplied(ctx context.Context, identity AgentIdentity, applied proto.Applied) {
	if applied.Refused != "" {
		s.recordAgentEvent(ctx, identity, "reconcile", applied.Refused, "warning")
		return
	}

	for _, created := range applied.Created {
		s.recordAgentEvent(ctx, identity, "reconcile", "Started "+created+".", "info")
	}
	for _, removed := range applied.Removed {
		s.recordAgentEvent(ctx, identity, "reconcile", "Removed "+removed+".", "info")
	}
	for _, failure := range applied.Failures {
		s.recordAgentEvent(ctx, identity, "reconcile",
			failure.Container+" "+failure.Reason+".", "error")
		slog.Error("agent could not apply part of a specification",
			"serverId", identity.ServerID, "container", failure.Container, "reason", failure.Reason)
	}
}
