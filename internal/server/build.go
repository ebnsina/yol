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
