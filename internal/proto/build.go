package proto

import "time"

// Builds happen on the customer's own machine. Nothing is uploaded to us and no build fleet is
// run on their behalf, which is what makes a deploy cost them nothing beyond the server they
// already pay for. It also means a build shares a machine with live traffic, so what a build may
// consume is decided here rather than left to it.

// BuildRequest asks a server to turn a commit into a runnable image.
type BuildRequest struct {
	DeploymentID string `json:"deploymentId"`
	ServiceID    string `json:"serviceId"`

	// The source as an archive of one commit rather than a repository to clone. A commit rather
	// than a branch, so what gets built is what was asked for even if the branch has moved on.
	// Fetching an archive also means a customer's server needs no version control installed, and
	// the trade is that a build cannot read its own history.
	SourceURL string `json:"sourceUrl"`
	CommitSHA string `json:"commitSha"`

	// Minted for this build alone and short lived. Sent as a header rather than in the address,
	// never written to disk, and empty for a repository that needs no credential.
	SourceToken string `json:"sourceToken,omitempty"`

	// Where in the repository the app lives, for one holding more than a single app.
	WorkDir string `json:"workDir,omitempty"`
	// A Dockerfile the user wrote always wins over anything we would infer. Relative to WorkDir.
	DockerfilePath string `json:"dockerfilePath,omitempty"`

	// What the finished image is called locally. Includes the commit, so a rollback is a matter
	// of running an image already on the machine rather than building again.
	ImageRef string            `json:"imageRef"`
	Labels   map[string]string `json:"labels,omitempty"`

	// Variables the build itself needs, which are not the same as the ones the app runs with.
	BuildEnv map[string]string `json:"buildEnv,omitempty"`

	// Limits for the build, not for the app. A build that took the whole machine would slow the
	// site it is being deployed for.
	MemoryLimitBytes int64 `json:"memoryLimitBytes,omitempty"`
	CPUPercent       int   `json:"cpuPercent,omitempty"`
	TimeoutSec       int   `json:"timeoutSec,omitempty"`
}

// Builder names how an image was produced, so the interface can say which path a build took.
type Builder string

const (
	BuilderDockerfile Builder = "dockerfile"
	BuilderNixpacks   Builder = "nixpacks"
)

// BuildOutput carries build output while it happens, batched the same way logs are. Watching a
// build is most of what a user does while waiting for one.
type BuildOutput struct {
	DeploymentID string    `json:"deploymentId"`
	At           time.Time `json:"at"`
	Lines        []LogLine `json:"lines,omitempty"`
}

// BuildResult ends a build either way. A failure carries the reason in plain language, because
// the whole point of waiting through a build is finding out what happened.
type BuildResult struct {
	DeploymentID string    `json:"deploymentId"`
	At           time.Time `json:"at"`
	Succeeded    bool      `json:"succeeded"`
	Builder      Builder   `json:"builder,omitempty"`
	ImageRef     string    `json:"imageRef,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}
