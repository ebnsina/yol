package proto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	sent := Hello{
		AgentVersion: "0.1.0",
		Capabilities: []Capability{CapInventory, CapReconcile},
		Facts:        HostFacts{Hostname: "server-1", OSName: "Ubuntu", CPUCount: 4},
		SpecVersion:  7,
	}

	raw, err := Encode(TypeHello, sent)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	env, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if env.Type != TypeHello {
		t.Errorf("type = %q, want %q", env.Type, TypeHello)
	}
	if env.Version != Version {
		t.Errorf("version = %d, want %d", env.Version, Version)
	}

	var received Hello
	if err := env.Into(&received); err != nil {
		t.Fatalf("Into: %v", err)
	}
	if received.AgentVersion != sent.AgentVersion || received.SpecVersion != sent.SpecVersion {
		t.Errorf("payload did not survive: %+v", received)
	}
}

// An older agent sends messages missing fields a newer control plane knows about. That must
// decode, leaving the unknown fields at their zero values.
func TestOlderAgentMessageStillDecodes(t *testing.T) {
	// As an agent predating Usage and Disks would have sent it.
	old := `{"v":1,"type":"heartbeat","payload":{"at":"2026-01-01T00:00:00Z","facts":{"hostname":"old-server"}}}`

	env, err := Decode([]byte(old))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var beat Heartbeat
	if err := env.Into(&beat); err != nil {
		t.Fatalf("Into: %v", err)
	}
	if beat.Facts.Hostname != "old-server" {
		t.Errorf("hostname = %q", beat.Facts.Hostname)
	}
	if beat.Usage.CPUPercent != 0 || len(beat.Disks) != 0 {
		t.Error("absent fields should be zero, not invented")
	}
}

// A newer agent will send fields this build has never heard of. Ignoring them is what lets a
// fleet be upgraded in any order.
func TestNewerAgentMessageStillDecodes(t *testing.T) {
	newer := `{"v":2,"type":"heartbeat","somethingNew":true,` +
		`"payload":{"at":"2026-01-01T00:00:00Z","facts":{"hostname":"new-server","gpuCount":2},"futureField":"ignored"}}`

	env, err := Decode([]byte(newer))
	if err != nil {
		t.Fatalf("a newer message must not be rejected: %v", err)
	}
	if env.Version != 2 {
		t.Errorf("version = %d, want the version as sent", env.Version)
	}

	var beat Heartbeat
	if err := env.Into(&beat); err != nil {
		t.Fatalf("Into: %v", err)
	}
	if beat.Facts.Hostname != "new-server" {
		t.Errorf("hostname = %q", beat.Facts.Hostname)
	}
}

func TestDecodeRejectsUnusableMessages(t *testing.T) {
	cases := map[string]string{
		"not json":            `{"v":1,`,
		"no type":             `{"v":1,"payload":{}}`,
		"empty":               ``,
		"json but not object": `[1,2,3]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(raw)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestIntoReportsMissingPayload(t *testing.T) {
	env, err := Decode([]byte(`{"v":1,"type":"heartbeat"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var beat Heartbeat
	if err := env.Into(&beat); err == nil {
		t.Error("expected an error for a message with no payload")
	}
}

func TestEncodeReplyEchoesRequestID(t *testing.T) {
	raw, err := EncodeReply(TypeApplied, "request-42", Applied{SpecVersion: 3, Unchanged: 2})
	if err != nil {
		t.Fatalf("EncodeReply: %v", err)
	}
	env, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if env.ReplyTo != "request-42" {
		t.Errorf("reply = %q, want request-42", env.ReplyTo)
	}
}

func TestManagedLabelsMarkOwnership(t *testing.T) {
	labels := ManagedLabels("org-1", "project-1", "env-1", "service-1", "deploy-1", RoleApp)

	if !IsManaged(labels) {
		t.Error("labels built for our own container are not recognised as managed")
	}
	for _, key := range []string{LabelOrg, LabelProject, LabelEnv, LabelService, LabelDeployment, LabelRole} {
		if labels[key] == "" {
			t.Errorf("label %q was not set", key)
		}
	}
}

// Empty identifiers must be left out rather than written as blanks, which would otherwise
// produce containers labelled with an empty project.
func TestManagedLabelsOmitsEmptyValues(t *testing.T) {
	labels := ManagedLabels("org-1", "", "", "", "", RoleRouter)

	if _, present := labels[LabelProject]; present {
		t.Error("an empty project was written as a label")
	}
	if labels[LabelRole] != RoleRouter {
		t.Errorf("role = %q, want %q", labels[LabelRole], RoleRouter)
	}
}

// A customer's own container carries no label of ours and must never look managed.
func TestUnlabelledContainerIsNotManaged(t *testing.T) {
	cases := []map[string]string{
		nil,
		{},
		{"com.docker.compose.project": "their-stack"},
		{LabelManaged: "false"},
		{LabelManaged: "TRUE"}, // only the exact value counts
		{LabelOrg: "org-1"},    // ours-looking, but not marked managed
	}
	for i, labels := range cases {
		if IsManaged(labels) {
			t.Errorf("case %d was treated as managed: %v", i, labels)
		}
	}
}

func TestSpecSerialisesWithoutLoss(t *testing.T) {
	spec := Spec{
		Version:  9,
		ServerID: "server-1",
		IssuedAt: time.Now().UTC().Truncate(time.Second),
		Containers: []SpecContainer{{
			Name:             "app-prod",
			Image:            "registry/app:abc123",
			Labels:           ManagedLabels("org-1", "p", "e", "s", "d", RoleApp),
			Ports:            []PortMapping{{HostPort: 30001, ContainerPort: 3000, Protocol: "tcp"}},
			MemoryLimitBytes: 512 << 20,
			RestartPolicy:    "unless-stopped",
			HealthCheck:      &HealthGate{HTTPPath: "/health", Port: 3000, TimeoutSec: 60, IntervalSec: 1},
		}},
		Adopted: []AdoptedContainer{{Name: "their-postgres", CreatedAt: time.Now().UTC().Truncate(time.Second)}},
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Spec
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.Version != spec.Version || len(back.Containers) != 1 {
		t.Fatalf("spec did not survive: %+v", back)
	}
	if back.Containers[0].MemoryLimitBytes != spec.Containers[0].MemoryLimitBytes {
		t.Error("memory limit lost, which would let one container starve a server")
	}
	if back.Containers[0].HealthCheck == nil {
		t.Error("health check lost, which would turn a deploy into a downtime")
	}
	if len(back.Adopted) != 1 || back.Adopted[0].Name != "their-postgres" {
		t.Error("adoption record lost, so an adopted container would look unmanaged")
	}
}

// Every message type must be a readable string, so a log line can be understood and a
// renumbering cannot silently change meaning.
func TestMessageTypesAreStableStrings(t *testing.T) {
	all := []Type{
		TypeHello, TypeHeartbeat, TypeInventory, TypeApplied, TypeLogChunk,
		TypeWelcome, TypeApplySpec, TypeTailLogs, TypeStopTail, TypeSurvey,
	}
	seen := make(map[Type]bool, len(all))
	for _, msgType := range all {
		if msgType == "" {
			t.Error("a message type is empty")
		}
		if strings.ToLower(string(msgType)) != string(msgType) {
			t.Errorf("%q should be lower case for consistency", msgType)
		}
		if seen[msgType] {
			t.Errorf("%q is duplicated", msgType)
		}
		seen[msgType] = true
	}
}

// Watch-only is a promise to the customer, so its value must never drift.
func TestModeValuesAreFixed(t *testing.T) {
	if ModeManaged != "managed" || ModeWatch != "watch" {
		t.Error("mode values changed, which would silently alter what an older agent permits")
	}
}
