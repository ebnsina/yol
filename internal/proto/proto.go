// Package proto is the contract between the control plane and the agents running on
// customer servers.
//
// The two are deployed independently and agents in the field are always older than the API,
// sometimes by months. Three rules keep that survivable:
//
//  1. Never remove or repurpose a field. Add new ones and leave old ones populated.
//  2. Never change the meaning of an existing value. Add a new value instead.
//  3. Gate anything new on the capabilities the agent reports, never on its version string.
//
// Unknown message types and unknown fields are ignored rather than treated as errors, so a
// newer control plane can talk to an older agent without either side failing.
package proto

import (
	"encoding/json"
	"fmt"
	"time"
)

// Version is the contract version this build speaks. Bumped only when the envelope itself
// changes, which should be almost never.
const Version = 1

// Capability names a behaviour an agent supports. The control plane checks for these rather
// than comparing versions, so a partially upgraded fleet behaves predictably.
type Capability string

const (
	CapInventory  Capability = "inventory"   // reports what is on the machine
	CapReconcile  Capability = "reconcile"   // applies a desired specification
	CapLogTail    Capability = "log-tail"    // streams container logs
	CapMetrics    Capability = "metrics"     // reports resource usage
	CapExecBackup Capability = "exec-backup" // runs backups
	CapBuild      Capability = "build"       // builds images from a repository
)

// Type identifies a message. Values are stable strings, never numbers, so a log line is
// readable and a renumbering cannot silently change meaning.
type Type string

const (
	// From the agent.
	TypeHello     Type = "hello"
	TypeHeartbeat Type = "heartbeat"
	TypeInventory Type = "inventory"
	TypeApplied   Type = "applied"
	TypeLogChunk  Type = "log-chunk"

	TypeBuildOutput Type = "build-output"
	TypeBuildResult Type = "build-result"

	// From the control plane.
	TypeWelcome   Type = "welcome"
	TypeApplySpec Type = "apply-spec"
	TypeBuild     Type = "build"
	TypeTailLogs  Type = "tail-logs"
	TypeStopTail  Type = "stop-tail"
	TypeSurvey    Type = "survey"
)

// Envelope wraps every message. Payload stays raw so an unknown Type can be skipped without
// failing to parse the rest.
type Envelope struct {
	Version int             `json:"v"`
	Type    Type            `json:"type"`
	ID      string          `json:"id,omitempty"`    // set on requests expecting a reply
	ReplyTo string          `json:"reply,omitempty"` // echoes the ID being answered
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Encode builds an envelope around a payload.
func Encode(msgType Type, payload any) ([]byte, error) {
	env := Envelope{Version: Version, Type: msgType}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("proto: encode %s: %w", msgType, err)
		}
		env.Payload = raw
	}
	return json.Marshal(env)
}

// EncodeReply builds an envelope answering a request.
func EncodeReply(msgType Type, replyTo string, payload any) ([]byte, error) {
	raw, err := Encode(msgType, payload)
	if err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	env.ReplyTo = replyTo
	return json.Marshal(env)
}

// Decode reads an envelope. A version newer than this build is accepted rather than
// rejected, because the envelope is designed to stay compatible and refusing would break a
// fleet mid-upgrade.
func Decode(data []byte) (*Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("proto: decode envelope: %w", err)
	}
	if env.Type == "" {
		return nil, fmt.Errorf("proto: message has no type")
	}
	return &env, nil
}

// Into unmarshals a payload into dst.
func (e *Envelope) Into(dst any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("proto: %s has no payload", e.Type)
	}
	if err := json.Unmarshal(e.Payload, dst); err != nil {
		return fmt.Errorf("proto: decode %s payload: %w", e.Type, err)
	}
	return nil
}

// Hello is the agent's first message, identifying itself and what it can do.
type Hello struct {
	AgentVersion string       `json:"agentVersion"`
	Capabilities []Capability `json:"capabilities"`
	Facts        HostFacts    `json:"facts"`
	// Version of the desired specification the agent currently has on disk, so the control
	// plane can skip resending an unchanged one.
	SpecVersion int64 `json:"specVersion"`
}

// Welcome is the control plane's answer. Mode tells the agent whether it may change anything
// at all, and the agent enforces it rather than trusting the control plane to only send
// permitted instructions.
type Welcome struct {
	ServerID     string       `json:"serverId"`
	Mode         Mode         `json:"mode"`
	Capabilities []Capability `json:"capabilities"` // what the control plane will use
	HeartbeatSec int          `json:"heartbeatSec"`
	InventorySec int          `json:"inventorySec"`
}

// Mode is how much the agent is permitted to do.
type Mode string

const (
	// ModeManaged allows creating and removing resources we own.
	ModeManaged Mode = "managed"
	// ModeWatch forbids every change. Enforced in the agent, so a control plane bug cannot
	// cause a write to a server someone asked us only to watch.
	ModeWatch Mode = "watch"
)

// HostFacts describes the machine. Refreshed on every heartbeat because disks fill and
// memory changes.
type HostFacts struct {
	Hostname      string `json:"hostname"`
	OSName        string `json:"osName"`
	OSVersion     string `json:"osVersion"`
	Kernel        string `json:"kernel"`
	Arch          string `json:"arch"`
	CPUCount      int    `json:"cpuCount"`
	MemoryBytes   int64  `json:"memoryBytes"`
	DockerVersion string `json:"dockerVersion,omitempty"`
}

// Heartbeat is the periodic liveness and usage report.
type Heartbeat struct {
	At          time.Time  `json:"at"`
	Facts       HostFacts  `json:"facts"`
	Usage       Usage      `json:"usage"`
	SpecVersion int64      `json:"specVersion"`
	Warnings    []string   `json:"warnings,omitempty"`
	Disks       []DiskFree `json:"disks,omitempty"`
}

// Usage is current resource consumption.
type Usage struct {
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryUsedBytes  int64   `json:"memoryUsedBytes"`
	MemoryTotalBytes int64   `json:"memoryTotalBytes"`
	LoadAverage1     float64 `json:"loadAverage1"`
}

// DiskFree is usage for one mount point.
type DiskFree struct {
	Mount      string `json:"mount"`
	TotalBytes int64  `json:"totalBytes"`
	UsedBytes  int64  `json:"usedBytes"`
}
