package ssh

import (
	"testing"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

func TestPortFromAddress(t *testing.T) {
	cases := map[string]struct {
		want int
		ok   bool
	}{
		"0.0.0.0:80":            {80, true},
		"[::]:443":              {443, true},
		"*:22":                  {22, true},
		"127.0.0.1:5432":        {5432, true},
		"[::ffff:0.0.0.0]:8080": {8080, true},
		"0.0.0.0:*":             {0, false},
		"nonsense":              {0, false},
		"0.0.0.0:99999":         {0, false},
		"0.0.0.0:0":             {0, false},
		"":                      {0, false},
	}
	for address, want := range cases {
		t.Run(address, func(t *testing.T) {
			got, ok := portFromAddress(address)
			if ok != want.ok || got != want.want {
				t.Errorf("= %d, %v; want %d, %v", got, ok, want.want, want.ok)
			}
		})
	}
}

func TestProcessName(t *testing.T) {
	cases := map[string]string{
		`tcp LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=1234,fd=6))`: "nginx",
		`tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=900,fd=3))`:   "sshd",
		`tcp LISTEN 0 4096 127.0.0.1:5432 0.0.0.0:*`:                            "",
		`malformed users:((`: "",
	}
	for line, want := range cases {
		if got := processName(line); got != want {
			t.Errorf("processName(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestParsePublishedPorts(t *testing.T) {
	cases := map[string][]int{
		"0.0.0.0:8080->80/tcp, :::8080->80/tcp":    {8080},
		"0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp": {80, 443},
		"80/tcp": nil, // exposed but not published
		"":       nil,
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			got := parsePublishedPorts(raw)
			if len(got) != len(want) {
				t.Fatalf("= %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("= %v, want %v", got, want)
				}
			}
		})
	}
}

func TestParseLabels(t *testing.T) {
	labels := parseLabels("yol.managed=true,yol.org=abc,com.docker.compose.project=their-stack")

	if labels["yol.managed"] != "true" {
		t.Errorf("managed = %q", labels["yol.managed"])
	}
	if labels["com.docker.compose.project"] != "their-stack" {
		t.Errorf("compose project = %q", labels["com.docker.compose.project"])
	}
	if parseLabels("") != nil {
		t.Error("empty label string should give nil, not an empty map")
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1.61GB": 1_610_000_000,
		"48.2MB": 48_200_000,
		"512kB":  512_000,
		"0B":     0,
		"":       0,
		"weird":  0,
	}
	for raw, want := range cases {
		if got := parseSize(raw); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestParseDockerTime(t *testing.T) {
	// Docker's CreatedAt is not RFC 3339, which is easy to get wrong.
	got := parseDockerTime("2026-07-30 11:36:03 +0600 +06")
	if got.IsZero() {
		t.Fatal("Docker's own timestamp format was not parsed")
	}
	if got.Year() != 2026 || got.Month() != time.July {
		t.Errorf("= %v", got)
	}
	if !parseDockerTime("not a time").IsZero() {
		t.Error("an unparseable time should be zero, not a guess")
	}
}

func TestFirstName(t *testing.T) {
	cases := map[string]string{
		"/my-app":        "my-app",
		"my-app":         "my-app",
		"/first,/second": "first",
		"":               "",
	}
	for raw, want := range cases {
		if got := firstName(raw); got != want {
			t.Errorf("firstName(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Only Ubuntu and Debian are supported, and the refusal must read as a sentence.
func TestUnsupported(t *testing.T) {
	supported := []string{"Ubuntu", "Debian GNU/Linux", "ubuntu"}
	for _, name := range supported {
		if reason := unsupported(proto.HostFacts{OSName: name}); reason != "" {
			t.Errorf("%s was rejected: %s", name, reason)
		}
	}

	for _, name := range []string{"Alpine Linux", "CentOS Linux", "Fedora", ""} {
		reason := unsupported(proto.HostFacts{OSName: name})
		if reason == "" {
			t.Errorf("%q should not be accepted yet", name)
			continue
		}
		if reason[len(reason)-1] != '.' {
			t.Errorf("reason for %q is not a sentence: %q", name, reason)
		}
	}
}

// A container's image name is the strongest signal, and should be reported as certain.
func TestDatabasesFromContainerImages(t *testing.T) {
	inv := proto.Inventory{Containers: []proto.Container{
		{Name: "their-db", Image: "postgres:15.4", State: "running", Ports: []int{5432}},
		{Name: "cache", Image: "redis:7-alpine", State: "running", Ports: []int{6379}},
	}}

	found := databases(inv)
	if len(found) != 2 {
		t.Fatalf("found %d databases, want 2: %+v", len(found), found)
	}
	for _, db := range found {
		if db.Confidence != proto.ConfidenceCertain {
			t.Errorf("%s reported as %q, want certain from an image name", db.Engine, db.Confidence)
		}
		if !db.InDocker {
			t.Errorf("%s should be marked as running in Docker", db.Engine)
		}
	}
	if found[0].Version != "15.4" {
		t.Errorf("version = %q, want 15.4 from the tag", found[0].Version)
	}
}

// A natively installed database shows up as a unit, which is a weaker signal than an image.
func TestDatabasesFromSystemdUnits(t *testing.T) {
	inv := proto.Inventory{Services: []proto.Service{
		{Name: "postgresql", Active: "active"},
		{Name: "nginx", Active: "active"},
	}}

	found := databases(inv)
	if len(found) != 1 {
		t.Fatalf("found %d, want 1: %+v", len(found), found)
	}
	if found[0].Engine != proto.EnginePostgres {
		t.Errorf("engine = %q", found[0].Engine)
	}
	if found[0].Confidence != proto.ConfidenceLikely {
		t.Errorf("confidence = %q, want likely for a unit name", found[0].Confidence)
	}
	if found[0].InDocker {
		t.Error("a unit is not running in Docker")
	}
}

// A bare open port is the weakest signal and must be reported as such, not as fact.
func TestDatabasesFromPortsAreOnlyPossible(t *testing.T) {
	inv := proto.Inventory{Ports: []proto.Port{{Number: 3306, Protocol: "tcp"}}}

	found := databases(inv)
	if len(found) != 1 {
		t.Fatalf("found %d, want 1", len(found))
	}
	if found[0].Confidence != proto.ConfidencePossible {
		t.Errorf("confidence = %q, want possible for a port alone", found[0].Confidence)
	}
	if found[0].Source != "an unidentified process" {
		t.Errorf("source = %q, should say we do not know", found[0].Source)
	}
}

// The same engine must not be reported three times because it matched a container, a unit and
// a port.
func TestDatabasesAreNotReportedTwice(t *testing.T) {
	inv := proto.Inventory{
		Containers: []proto.Container{{Name: "db", Image: "postgres:16", State: "running", Ports: []int{5432}}},
		Services:   []proto.Service{{Name: "postgresql", Active: "active"}},
		Ports:      []proto.Port{{Number: 5432, Protocol: "tcp", Process: "docker-proxy"}},
	}

	if found := databases(inv); len(found) != 1 {
		t.Errorf("found %d entries for one database: %+v", len(found), found)
	}
}

// The whole reason for the survey: know who holds 80 and 443 before touching anything.
func TestConflictsIdentifyWhatHoldsRouterPorts(t *testing.T) {
	inv := proto.Inventory{
		Ports: []proto.Port{
			{Number: 80, Protocol: "tcp", Process: "nginx"},
			{Number: 443, Protocol: "tcp", Process: "nginx"},
			{Number: 22, Protocol: "tcp", Process: "sshd"},
		},
	}

	found := conflicts(inv)
	if len(found) != 2 {
		t.Fatalf("found %d conflicts, want 2: %+v", len(found), found)
	}
	for _, conflict := range found {
		if conflict.Process != "nginx" {
			t.Errorf("port %d attributed to %q", conflict.Port, conflict.Process)
		}
	}
}

// When a container holds the port, name it, because "nginx" is not actionable but
// "the container called their-proxy" is.
func TestConflictsNameTheContainerHoldingThePort(t *testing.T) {
	inv := proto.Inventory{
		Ports:      []proto.Port{{Number: 80, Protocol: "tcp", Process: "docker-proxy"}},
		Containers: []proto.Container{{Name: "their-proxy", State: "running", Ports: []int{80}}},
	}

	found := conflicts(inv)
	if len(found) != 1 {
		t.Fatalf("found %d conflicts, want 1", len(found))
	}
	if found[0].Container != "their-proxy" {
		t.Errorf("container = %q, want their-proxy", found[0].Container)
	}
}

func TestNoConflictsOnAFreeServer(t *testing.T) {
	inv := proto.Inventory{Ports: []proto.Port{{Number: 22, Protocol: "tcp", Process: "sshd"}}}
	if found := conflicts(inv); len(found) != 0 {
		t.Errorf("reported %d conflicts on a server with only ssh: %+v", len(found), found)
	}
}

// A stopped container is not holding anything, so it must not be blamed for a conflict.
func TestStoppedContainersAreNotBlamedForConflicts(t *testing.T) {
	inv := proto.Inventory{
		Ports:      []proto.Port{{Number: 80, Protocol: "tcp", Process: "nginx"}},
		Containers: []proto.Container{{Name: "old-proxy", State: "exited", Ports: []int{80}}},
	}

	found := conflicts(inv)
	if len(found) != 1 {
		t.Fatalf("found %d conflicts", len(found))
	}
	if found[0].Container != "" {
		t.Errorf("a stopped container was blamed: %q", found[0].Container)
	}
}

func TestTargetAddr(t *testing.T) {
	cases := map[Target]string{
		{Host: "203.0.113.9"}:                  "203.0.113.9:22",
		{Host: "203.0.113.9", Port: 2222}:      "203.0.113.9:2222",
		{Host: "2001:db8::1", Port: 22}:        "[2001:db8::1]:22",
		{Host: "server.example.com", Port: 22}: "server.example.com:22",
	}
	for target, want := range cases {
		if got := target.Addr(); got != want {
			t.Errorf("Addr() = %q, want %q", got, want)
		}
	}
}

func TestAuthMethodsRequiresACredential(t *testing.T) {
	if _, err := authMethods(Credential{User: "root"}); err == nil {
		t.Error("no key and no password should be an error")
	}
	if _, err := authMethods(Credential{User: "root", Key: "not a key"}); err == nil {
		t.Error("an unreadable key should be an error")
	}
	if _, err := authMethods(Credential{User: "root", Password: "hunter2"}); err != nil {
		t.Errorf("a password should be accepted: %v", err)
	}
}
