package proto

import "time"

// Inventory is everything the agent found on the machine, whether we created it or not.
// This is what lets the interface answer "what is on this server?" rather than presenting a
// machine as empty when it is not.
type Inventory struct {
	At         time.Time   `json:"at"`
	Containers []Container `json:"containers"`
	Images     []Image     `json:"images"`
	Volumes    []Volume    `json:"volumes"`
	Services   []Service   `json:"services"`
	Ports      []Port      `json:"ports"`
	Databases  []Database  `json:"databases"`

	// Set when the survey could not read something, so the interface can say the picture is
	// incomplete instead of implying the machine has nothing.
	Incomplete []string `json:"incomplete,omitempty"`
}

// Container is one container, ours or the customer's.
type Container struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Status    string            `json:"status"`
	State     string            `json:"state"`
	CreatedAt time.Time         `json:"createdAt"`
	Ports     []int             `json:"ports,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`

	// True when it carries our ownership label. Anything false is reported but never
	// changed unless the user explicitly adopts it.
	Managed bool `json:"managed"`

	// Present when the container belongs to a compose project, which is a strong hint that
	// it was set up by hand and should be offered for adoption as a group.
	ComposeProject string `json:"composeProject,omitempty"`
}

// Image is a stored image and what it costs in disk.
type Image struct {
	ID        string    `json:"id"`
	Tags      []string  `json:"tags,omitempty"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
}

// Volume is a named volume. Reported because a volume nobody references is often the reason
// a disk is unexpectedly full.
type Volume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	InUse      bool   `json:"inUse"`
	Managed    bool   `json:"managed"`
	Mountpoint string `json:"mountpoint,omitempty"`
}

// Service is a systemd unit. Included because it is how someone recognises a machine they
// configured months ago.
type Service struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Active      string `json:"active"`
	Enabled     string `json:"enabled"`
}

// Port is something listening, and what is holding it. This is what makes a port conflict
// explainable rather than mysterious.
type Port struct {
	Number   int    `json:"number"`
	Protocol string `json:"protocol"`
	Process  string `json:"process,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Address  string `json:"address,omitempty"`
	// Set when the listener is a container rather than a process on the host.
	Container string `json:"container,omitempty"`
}

// DatabaseEngine is a kind of database we know how to recognise.
type DatabaseEngine string

const (
	EnginePostgres   DatabaseEngine = "postgres"
	EngineMySQL      DatabaseEngine = "mysql"
	EngineRedis      DatabaseEngine = "redis"
	EngineClickHouse DatabaseEngine = "clickhouse"
	EngineMongo      DatabaseEngine = "mongodb"
)

// Database is something that looks like a database. Detection is by nature a guess, so
// Confidence is reported and the interface presents these as "we found something that looks
// like" rather than as fact. Nothing is ever acted on automatically.
type Database struct {
	Engine     DatabaseEngine `json:"engine"`
	Version    string         `json:"version,omitempty"`
	Port       int            `json:"port,omitempty"`
	Confidence Confidence     `json:"confidence"`
	// Where it came from: a container name, or a systemd unit for one installed natively.
	Source   string `json:"source"`
	InDocker bool   `json:"inDocker"`
	DataPath string `json:"dataPath,omitempty"`
	Managed  bool   `json:"managed"`
}

// Confidence is how sure the detection is.
type Confidence string

const (
	// ConfidenceCertain means the container image or unit says so unambiguously.
	ConfidenceCertain Confidence = "certain"
	// ConfidenceLikely means a well-known port or process name matched.
	ConfidenceLikely Confidence = "likely"
	// ConfidencePossible means it is a guess worth showing but easily wrong.
	ConfidencePossible Confidence = "possible"
)

// RoutingConflict describes something already holding the ports our router needs. Reported
// by the survey so the user is asked rather than having a decision made for them.
type RoutingConflict struct {
	Port      int    `json:"port"`
	Process   string `json:"process,omitempty"`
	Container string `json:"container,omitempty"`
	Unit      string `json:"unit,omitempty"`
}

// SurveyResult is the whole picture from a first look at a server, before anything on it has
// been changed. Cancelling at this point leaves the machine untouched.
type SurveyResult struct {
	Facts     HostFacts         `json:"facts"`
	Inventory Inventory         `json:"inventory"`
	Conflicts []RoutingConflict `json:"conflicts,omitempty"`

	DockerPresent bool `json:"dockerPresent"`
	// Set when the machine is not one we support, in the same plain language shown to users.
	Unsupported string `json:"unsupported,omitempty"`
}
