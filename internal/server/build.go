package server

import (
	"context"
	"fmt"

	"github.com/ebnsina/yol/internal/proto"
	"github.com/google/uuid"
)

// Builds run on the customer's own machine, so the control plane's part is to ask for one and to
// keep what came back. Nothing here touches Docker or reads any code.

// DeployRecorder keeps what happened while deploying. An interface so this package does not need to
// know how deployments are stored, and so a deploy can be exercised without a database.
type DeployRecorder interface {
	// RecordBuildOutput keeps output as it arrives, which is what a user watches while waiting.
	RecordBuildOutput(ctx context.Context, orgID uuid.UUID, output proto.BuildOutput)
	// FinishBuild records how a build ended, either way.
	FinishBuild(ctx context.Context, orgID uuid.UUID, result proto.BuildResult)
	// FinishRollout records whether a version began serving once it was started.
	FinishRollout(ctx context.Context, orgID uuid.UUID, rollout proto.Rollout)
}

// Dispatcher is how work reaches a server. It exists so the part of the system that decides what to
// deploy needs to know nothing about connections, signing, or which agents happen to be online.
type Dispatcher struct {
	svc    *Service
	hub    *Hub
	signer *proto.SigningKey
}

// NewDispatcher builds it.
func NewDispatcher(svc *Service, hub *Hub, signer *proto.SigningKey) *Dispatcher {
	return &Dispatcher{svc: svc, hub: hub, signer: signer}
}

// Build asks a server to turn a commit into an image.
func (d *Dispatcher) Build(ctx context.Context, serverID uuid.UUID, req proto.BuildRequest) error {
	conn, ok := d.hub.Get(serverID)
	if !ok {
		return fmt.Errorf("server: %s is not connected", serverID)
	}
	return SendBuild(ctx, conn, d.signer, req)
}

// Reconcile hands a server its desired state again, which is what sets a rollout going once an
// image exists. A server that is not connected needs nothing: it asks for its state when it
// returns, so there is nothing to remember on its behalf.
func (d *Dispatcher) Reconcile(ctx context.Context, serverID uuid.UUID) error {
	conn, ok := d.hub.Get(serverID)
	if !ok {
		return nil
	}
	return d.svc.SendSpec(ctx, conn, d.signer)
}

// SendBuild asks a connected agent to build a commit.
//
// Signed for the same reason a specification is: the request carries a credential for the
// repository and causes code to run, so reaching the connection must not be enough to start one.
func SendBuild(
	ctx context.Context,
	conn *Connection,
	signer *proto.SigningKey,
	req proto.BuildRequest,
) error {
	if !conn.Supports(proto.CapBuild) {
		return fmt.Errorf("server: the agent on this server is too old to build")
	}

	signed, err := signer.SignMessage(req)
	if err != nil {
		return err
	}
	return conn.Send(ctx, proto.TypeBuild, signed)
}
