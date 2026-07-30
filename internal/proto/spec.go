package proto

import "time"

// Ownership labels. The agent's destructive path only ever considers containers carrying
// LabelManaged, which is what keeps a customer's own containers safe from it.
const (
	LabelManaged    = "yol.managed"
	LabelOrg        = "yol.org"
	LabelProject    = "yol.project"
	LabelEnv        = "yol.env"
	LabelService    = "yol.service"
	LabelDeployment = "yol.deployment"
	LabelRole       = "yol.role"
)

// Roles a managed container can play.
const (
	RoleApp     = "app"
	RoleService = "service"
	RoleRouter  = "router"
)

// Spec is the desired state of one server. The agent makes the machine match it and reports
// what it did. Signed by the control plane; the agent verifies before applying, so control of
// the transport alone is not enough to run containers on a customer's machine.
type Spec struct {
	Version    int64           `json:"version"`
	ServerID   string          `json:"serverId"`
	IssuedAt   time.Time       `json:"issuedAt"`
	Containers []SpecContainer `json:"containers"`
	Volumes    []SpecVolume    `json:"volumes"`
	Routes     []SpecRoute     `json:"routes"`
	// Names of containers the user has adopted. The agent treats these as managed even
	// though they carry no label, because a label cannot be added to an existing container.
	Adopted []AdoptedContainer `json:"adopted,omitempty"`
}

// AdoptedContainer identifies a pre-existing container now under management. CreatedAt
// distinguishes it from a different container that happens to reuse the name.
type AdoptedContainer struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// SpecContainer is one container we want running.
type SpecContainer struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Labels  map[string]string `json:"labels"`

	Ports   []PortMapping `json:"ports,omitempty"`
	Mounts  []Mount       `json:"mounts,omitempty"`
	Network string        `json:"network,omitempty"`

	// Always set. One customer's runaway process must not take down everything else on
	// their server, or the platform gets blamed for their bug.
	MemoryLimitBytes int64 `json:"memoryLimitBytes"`
	CPUShares        int64 `json:"cpuShares,omitempty"`

	RestartPolicy string      `json:"restartPolicy"`
	HealthCheck   *HealthGate `json:"healthCheck,omitempty"`
}

// PortMapping publishes a container port on the host. The control plane allocates host ports
// so two projects on one server cannot collide.
type PortMapping struct {
	HostPort      int    `json:"hostPort"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

// Mount attaches a volume or host path.
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// SpecVolume is a volume we want to exist. Never removed automatically, because a volume
// holds the only copy of someone's data.
type SpecVolume struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// HealthGate is how the agent decides a new container is actually serving before traffic is
// moved to it. Without this a deploy is just a downtime with extra steps.
type HealthGate struct {
	// Either an HTTP path or a TCP port, not both.
	HTTPPath    string `json:"httpPath,omitempty"`
	TCPPort     int    `json:"tcpPort,omitempty"`
	Port        int    `json:"port,omitempty"`
	TimeoutSec  int    `json:"timeoutSec"`
	IntervalSec int    `json:"intervalSec"`
}

// SpecRoute maps a hostname to a container for the router.
type SpecRoute struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	Port      int    `json:"port"`
}

// Applied is the agent's report after a reconcile pass. Sent even when nothing changed, so
// the control plane can tell a quiet server from a stuck one.
type Applied struct {
	SpecVersion int64     `json:"specVersion"`
	At          time.Time `json:"at"`
	Created     []string  `json:"created,omitempty"`
	Removed     []string  `json:"removed,omitempty"`
	Unchanged   int       `json:"unchanged"`
	// Populated when something could not be applied, in plain language, so the interface can
	// explain a failed deploy rather than showing a spinner forever.
	Failures []ApplyFailure `json:"failures,omitempty"`
	// Set when the agent refused because it is in watch-only mode.
	Refused string `json:"refused,omitempty"`
}

// ApplyFailure is one thing that did not work.
type ApplyFailure struct {
	Container string `json:"container"`
	Reason    string `json:"reason"`
}

// TailLogs asks the agent to stream a container's logs. Allowed for any container, ours or
// not, because reading is not changing.
type TailLogs struct {
	StreamID  string `json:"streamId"`
	Container string `json:"container"`
	TailLines int    `json:"tailLines"`
	Follow    bool   `json:"follow"`
}

// StopTail ends a stream.
type StopTail struct {
	StreamID string `json:"streamId"`
}

// LogChunk is a batch of log lines.
type LogChunk struct {
	StreamID string    `json:"streamId"`
	At       time.Time `json:"at"`
	Lines    []LogLine `json:"lines"`
	// Set once the stream has ended, with a reason when it ended unexpectedly.
	Done   bool   `json:"done,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// LogLine is one line, tagged with which stream it came from.
type LogLine struct {
	At     time.Time `json:"at"`
	Stream string    `json:"stream"` // stdout or stderr
	Text   string    `json:"text"`
}

// ManagedLabels builds the ownership labels for a container we create.
func ManagedLabels(orgID, projectID, envID, serviceID, deploymentID, role string) map[string]string {
	labels := map[string]string{
		LabelManaged: "true",
		LabelOrg:     orgID,
		LabelRole:    role,
	}
	for key, value := range map[string]string{
		LabelProject:    projectID,
		LabelEnv:        envID,
		LabelService:    serviceID,
		LabelDeployment: deploymentID,
	} {
		if value != "" {
			labels[key] = value
		}
	}
	return labels
}

// IsManaged reports whether a container's labels mark it as ours.
func IsManaged(labels map[string]string) bool {
	return labels[LabelManaged] == "true"
}
