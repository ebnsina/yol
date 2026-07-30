package server

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/ebnsina/yol/internal/db/sqlc"
	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/proto"
	"github.com/ebnsina/yol/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The router that serves web traffic. It is the first thing this platform runs on a customer's
// server, and its ports depend on the answer they gave about ports 80 and 443.
const (
	routerContainer = "yol-router"
	routerImage     = "caddy:2-alpine"
	routerMemory    = 128 << 20

	// RouterAdminPort is where the router takes its configuration. Published on loopback only,
	// so nothing outside the machine can reconfigure how a customer's traffic is served.
	RouterAdminPort = 2019
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

		// What each service on this server should be running, from the deployment that is live.
		// A container is named for its deployment, so the next one is started alongside rather
		// than in place of it, which is what leaves the old version serving until the new one
		// answers.
		placements, err := q.ListLivePlacementsForServer(ctx, identity.ServerID)
		if err != nil {
			return err
		}
		serving := make(map[uuid.UUID]sqlc.ListLivePlacementsForServerRow, len(placements))
		for _, placement := range placements {
			if placement.ImageRef == nil {
				continue // built nothing yet, so there is nothing to run
			}

			// Where traffic goes. A version going out is started and checked, but nothing is sent to
			// it until it is the one serving, so a version that never answers is invisible to
			// anybody using the app.
			existing, seen := serving[placement.ServiceID]
			if !seen || (existing.Status != sqlc.DeploymentStatusLive &&
				placement.Status == sqlc.DeploymentStatusLive) {
				serving[placement.ServiceID] = placement
			}

			// What the app runs with. Read here rather than kept anywhere in the open: they are
			// decrypted only to be handed to the machine that runs the app.
			variables, err := s.variablesFor(ctx, q, placement.EnvironmentID)
			if err != nil {
				return err
			}
			container := containerFor(placement, identity.OrgID)
			container.Env = variables
			spec.Containers = append(spec.Containers, container)
		}

		if router := routerFor(row, identity.OrgID); router != nil {
			spec.Containers = append(spec.Containers, *router)
			spec.Volumes = append(spec.Volumes, proto.SpecVolume{
				Name:   routerContainer + "-data",
				Labels: proto.ManagedLabels(identity.OrgID.String(), "", "", "", "", proto.RoleRouter),
			})
			spec.Router = &proto.SpecRouter{
				AdminPort:     RouterAdminPort,
				PermissionURL: s.permissionURL,
			}

			// Hostnames this server answers for, and where each one goes.
			domains, err := q.ListDomainsForServer(ctx, &identity.ServerID)
			if err != nil {
				return err
			}
			for _, domain := range domains {
				placement, running := serving[domain.ServiceID]
				if !running {
					continue // nothing deployed yet, so there is nowhere to send the hostname
				}
				spec.Routes = append(spec.Routes, proto.SpecRoute{
					Host:      domain.Hostname,
					Container: placement.ContainerName,
					Port:      int(placement.Port),
				})
			}

			if fallback := fallbackRoute(serving); fallback != nil {
				spec.Routes = append(spec.Routes, *fallback)
			}
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

// variablesFor reads what an app runs with. A value that cannot be decrypted fails the whole
// specification rather than being left out, because an app started without one of its variables
// fails in ways that are far harder to understand than a specification that did not arrive.
func (s *Service) variablesFor(ctx context.Context, q *sqlc.Queries, envID uuid.UUID) (map[string]string, error) {
	rows, err := q.ListEnvVars(ctx, envID)
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		value, err := s.box.OpenString(row.Value, secrets.ContextEnvVar)
		if err != nil {
			return nil, httpx.Internal(err)
		}
		out[row.Name] = value
	}
	return out, nil
}

// containerFor describes what one service should be running, taken from its live deployment.
//
// Nothing is published to the machine: the router reaches an app by name over the private network,
// so an app is not exposed to the world except through the router.
func containerFor(placement sqlc.ListLivePlacementsForServerRow, orgID uuid.UUID) proto.SpecContainer {
	container := proto.SpecContainer{
		Name:  placement.ContainerName,
		Image: *placement.ImageRef,
		Labels: proto.ManagedLabels(orgID.String(), placement.ProjectID.String(),
			placement.EnvironmentID.String(), placement.ServiceID.String(),
			placement.DeploymentID.String(), proto.RoleApp),
		Network:          proto.Network,
		MemoryLimitBytes: placement.MemoryLimitBytes,
		RestartPolicy:    "unless-stopped",
	}

	// How the agent decides this version is serving before traffic is moved onto it. A path is
	// asked for when the service named one, since accepting a connection is not the same as being
	// able to answer a request.
	gate := &proto.HealthGate{Port: int(placement.Port)}
	if placement.HealthPath != nil && *placement.HealthPath != "" {
		gate.HTTPPath = *placement.HealthPath
	}
	container.HealthCheck = gate
	return container
}

// fallbackRoute is where requests arriving by the server's address go, so an app can be opened
// before a domain has been added to it.
//
// Only when the server runs a single app is this unambiguous. With several, an address says nothing
// about which one was meant, so nothing is served by address and each is reached by its own name.
func fallbackRoute(serving map[uuid.UUID]sqlc.ListLivePlacementsForServerRow) *proto.SpecRoute {
	var only sqlc.ListLivePlacementsForServerRow
	found := 0
	for _, placement := range serving {
		if placement.Kind != sqlc.ServiceKindApp || placement.ImageRef == nil {
			continue
		}
		only = placement
		found++
	}
	if found != 1 {
		return nil
	}
	return &proto.SpecRoute{Container: only.ContainerName, Port: int(only.Port)}
}

// ContainerNameFor is how a service's container is named on a machine. The deployment is part of
// the name, so a new version is started alongside the one serving rather than in place of it.
func ContainerNameFor(serviceID, deploymentID uuid.UUID) string {
	return "yol-" + serviceID.String()[:12] + "-" + deploymentID.String()[:8]
}

// routerFor describes the router, or nothing when this server does not need one.
func routerFor(row sqlc.Server, orgID uuid.UUID) *proto.SpecContainer {
	// Behind their own web server we would need ports allocated for it, which comes with
	// deployments. Until then only a server where we handle web traffic gets a router.
	if row.RoutingMode == nil || *row.RoutingMode != sqlc.RoutingModeTakeover {
		return nil
	}

	return &proto.SpecContainer{
		Name:  routerContainer,
		Image: routerImage,
		// Listening on every address is how the control interface becomes reachable from the
		// host at all; publishing it on loopback is what keeps it reachable only by the agent.
		Env:     map[string]string{"CADDY_ADMIN": "0.0.0.0:" + strconv.Itoa(RouterAdminPort)},
		Labels:  proto.ManagedLabels(orgID.String(), "", "", "", "", proto.RoleRouter),
		Network: proto.Network,
		Ports: []proto.PortMapping{
			{HostPort: 80, ContainerPort: 80, Protocol: "tcp"},
			{HostPort: 443, ContainerPort: 443, Protocol: "tcp"},
			{HostPort: RouterAdminPort, ContainerPort: RouterAdminPort, Protocol: "tcp", HostIP: "127.0.0.1"},
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
