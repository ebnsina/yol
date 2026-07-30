package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// stubCollector reports a fixed picture of a machine, so ownership can be checked without
// touching Docker.
type stubCollector struct {
	containers []proto.Container
}

func (s *stubCollector) Facts(context.Context) proto.HostFacts { return proto.HostFacts{} }
func (s *stubCollector) Usage(context.Context) proto.Usage     { return proto.Usage{} }
func (s *stubCollector) Inventory(context.Context) proto.Inventory {
	return proto.Inventory{Containers: s.containers}
}

// The rule the whole product rests on: the destructive step can only see what we own, so a
// customer's containers are not merely spared, they are invisible to it.
func TestOnlyOwnedContainersAreVisibleToRemoval(t *testing.T) {
	a := &Agent{
		mode: proto.ModeManaged,
		collector: &stubCollector{containers: []proto.Container{
			{Name: "yol-router", Managed: true},
			{Name: "their-nginx", Managed: false},
			{Name: "their-postgres", Managed: false},
			{Name: "old-worker", Managed: false},
		}},
	}

	owned := a.managedContainers(context.Background(), &proto.Spec{})

	if len(owned) != 1 {
		t.Fatalf("%d containers considered ours, want 1: %v", len(owned), keys(owned))
	}
	if _, ok := owned["yol-router"]; !ok {
		t.Error("our own container was not recognised")
	}
	for _, theirs := range []string{"their-nginx", "their-postgres", "old-worker"} {
		if _, ok := owned[theirs]; ok {
			t.Errorf("%s was treated as ours", theirs)
		}
	}
}

// An adopted container carries no label of ours, because a label cannot be added to a container
// that already exists, so it is recognised from the specification instead.
func TestAdoptedContainersAreTreatedAsOurs(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	a := &Agent{
		mode: proto.ModeManaged,
		collector: &stubCollector{containers: []proto.Container{
			{Name: "their-postgres", Managed: false, CreatedAt: created},
			{Name: "their-nginx", Managed: false, CreatedAt: created},
		}},
	}

	spec := &proto.Spec{Adopted: []proto.AdoptedContainer{{Name: "their-postgres", CreatedAt: created}}}
	owned := a.managedContainers(context.Background(), spec)

	if _, ok := owned["their-postgres"]; !ok {
		t.Error("an adopted container was not recognised as ours")
	}
	if _, ok := owned["their-nginx"]; ok {
		t.Error("a container that was not adopted was treated as ours")
	}
}

// A container reusing an adopted name must not inherit the adoption, or replacing one by hand
// would silently hand it to us.
func TestAdoptionDoesNotFollowAReusedName(t *testing.T) {
	adoptedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	replaced := time.Now().UTC().Truncate(time.Second)

	a := &Agent{
		mode: proto.ModeManaged,
		collector: &stubCollector{containers: []proto.Container{
			{Name: "their-postgres", Managed: false, CreatedAt: replaced},
		}},
	}

	spec := &proto.Spec{Adopted: []proto.AdoptedContainer{{Name: "their-postgres", CreatedAt: adoptedAt}}}
	if _, ok := a.managedContainers(context.Background(), spec)["their-postgres"]; ok {
		t.Error("a different container with the same name inherited the adoption")
	}
}

// A watched server must refuse to apply anything, decided by the agent itself.
func TestApplyRefusesOnAWatchedServer(t *testing.T) {
	a := &Agent{mode: proto.ModeWatch, collector: &stubCollector{}}

	applied := a.apply(context.Background(), &proto.Spec{
		Version:    7,
		Containers: []proto.SpecContainer{{Name: "app", Image: "ours:1"}},
	})

	if applied.Refused == "" {
		t.Error("a watched server did not refuse")
	}
	if len(applied.Created) > 0 || len(applied.Removed) > 0 {
		t.Errorf("a watched server reported changes: %+v", applied)
	}
}

// An agent with no mode set must also refuse, so failing to learn the mode is safe.
func TestApplyRefusesWithNoModeSet(t *testing.T) {
	a := &Agent{collector: &stubCollector{}}

	if applied := a.apply(context.Background(), &proto.Spec{Version: 1}); applied.Refused == "" {
		t.Error("an agent with no mode set did not refuse")
	}
}

func TestMatchesDetectsWhatNeedsReplacing(t *testing.T) {
	want := proto.SpecContainer{
		Name:   "app",
		Image:  "registry/app:abc",
		Labels: map[string]string{proto.LabelDeployment: "deploy-1"},
		Ports:  []proto.PortMapping{{HostPort: 30001, ContainerPort: 3000}},
	}
	running := proto.Container{
		Name:   "app",
		Image:  "registry/app:abc",
		State:  "running",
		Labels: map[string]string{proto.LabelDeployment: "deploy-1"},
		Ports:  []int{30001},
	}

	if !matches(running, want) {
		t.Error("an already-correct container was reported as needing replacement")
	}

	// A new deployment must replace the container, or the old version keeps serving.
	newer := want
	newer.Labels = map[string]string{proto.LabelDeployment: "deploy-2"}
	if matches(running, newer) {
		t.Error("a new deployment did not require replacing the container")
	}

	stopped := running
	stopped.State = "exited"
	if matches(stopped, want) {
		t.Error("a stopped container was treated as already correct")
	}

	rebuilt := want
	rebuilt.Image = "registry/app:def"
	if matches(running, rebuilt) {
		t.Error("a different image did not require replacing the container")
	}

	moved := want
	moved.Ports = []proto.PortMapping{{HostPort: 30002, ContainerPort: 3000}}
	if matches(running, moved) {
		t.Error("a different published port did not require replacing the container")
	}
}

func keys(m map[string]proto.Container) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	return out
}

// Docker often ends a failure with a suggestion to read its help, which tells nobody anything. The
// line that says what actually happened is what gets reported.
func TestAFailureReportsWhatWentWrongRatherThanWhereToReadAboutIt(t *testing.T) {
	output := `docker: Error response from daemon: Conflict. The container name "/yol-abc" is already in use.

Run 'docker run --help' for more information`

	got := lastLine(output)
	if !strings.Contains(got, "already in use") {
		t.Errorf("reported %q, want the reason it failed", got)
	}
}
