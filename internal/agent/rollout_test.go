package agent

import (
	"slices"
	"testing"

	"github.com/ebnsina/yol/internal/proto"
)

func deployed(name, deployment string) proto.SpecContainer {
	return proto.SpecContainer{
		Name:        name,
		Image:       "yol/app:" + deployment,
		Labels:      map[string]string{proto.LabelDeployment: deployment},
		HealthCheck: &proto.HealthGate{Port: 3000},
	}
}

func running(name, deployment string) proto.Container {
	return proto.Container{
		Name:    name,
		Image:   "yol/app:" + deployment,
		State:   "running",
		Managed: true,
		Labels:  map[string]string{proto.LabelDeployment: deployment},
	}
}

// The point of naming a container for its deployment: the new version is something to create
// alongside the old, not something that replaces it in place.
func TestANewVersionIsStartedAlongsideTheOldOne(t *testing.T) {
	actual := map[string]proto.Container{"app-old": running("app-old", "one")}
	spec := &proto.Spec{Containers: []proto.SpecContainer{deployed("app-new", "two")}}

	plan := planRollout(actual, spec, nil)

	if len(plan.create) != 1 || plan.create[0].Name != "app-new" {
		t.Errorf("create = %+v, want the new version started", plan.create)
	}
	if len(plan.replace) != 0 {
		t.Errorf("replace = %+v, want nothing interrupted", plan.replace)
	}
	// The old one is planned for removal, but the order apply follows is what makes that safe.
	if !slices.Equal(plan.remove, []string{"app-old"}) {
		t.Errorf("remove = %v, want the old version retired", plan.remove)
	}
}

// A container already as wanted is left completely alone, which is what lets this run every few
// seconds without touching anything.
func TestAnUnchangedServerIsLeftAlone(t *testing.T) {
	actual := map[string]proto.Container{"app": running("app", "one")}
	spec := &proto.Spec{Containers: []proto.SpecContainer{deployed("app", "one")}}

	plan := planRollout(actual, spec, nil)

	if plan.unchanged != 1 {
		t.Errorf("unchanged = %d, want the running container recognised", plan.unchanged)
	}
	if len(plan.create)+len(plan.replace)+len(plan.remove) != 0 {
		t.Errorf("plan = %+v, want nothing to do", plan)
	}
}

// A container keeping its name but changing shape cannot be altered in place, so it is interrupted.
// Databases keep a stable name, which is why they take this path and apps do not.
func TestAContainerKeepingItsNameIsReplaced(t *testing.T) {
	actual := map[string]proto.Container{"db": running("db", "one")}
	spec := &proto.Spec{Containers: []proto.SpecContainer{deployed("db", "two")}}

	plan := planRollout(actual, spec, nil)

	if len(plan.replace) != 1 || plan.replace[0].Name != "db" {
		t.Errorf("replace = %+v, want the container replaced under the same name", plan.replace)
	}
	if len(plan.remove) != 0 {
		t.Errorf("remove = %v, want it not also removed", plan.remove)
	}
}

// Anything of ours that is no longer wanted is retired, which is how a deleted service disappears.
func TestWhatIsNoLongerWantedIsRetired(t *testing.T) {
	actual := map[string]proto.Container{
		"gone-one": running("gone-one", "a"),
		"gone-two": running("gone-two", "b"),
		"kept":     running("kept", "c"),
	}
	spec := &proto.Spec{Containers: []proto.SpecContainer{deployed("kept", "c")}}

	plan := planRollout(actual, spec, nil)

	if !slices.Equal(plan.remove, []string{"gone-one", "gone-two"}) {
		t.Errorf("remove = %v, want both retired and in a settled order", plan.remove)
	}
}

// An empty specification on a machine running our containers means remove them, and must not be
// read as nothing to do.
func TestAnEmptySpecificationRetiresEverythingOfOurs(t *testing.T) {
	actual := map[string]proto.Container{"app": running("app", "one")}

	plan := planRollout(actual, &proto.Spec{}, nil)

	if !slices.Equal(plan.remove, []string{"app"}) {
		t.Errorf("remove = %v, want our container retired", plan.remove)
	}
}

// The rule that makes a failed deploy harmless: traffic is never pointed at a version that did not
// answer, so the one already serving keeps serving.
func TestTrafficIsNotMovedToAVersionThatDidNotAnswer(t *testing.T) {
	spec := &proto.Spec{
		Routes: []proto.SpecRoute{
			{Host: "app.example.com", Container: "app-new", Port: 3000},
			{Host: "other.example.com", Container: "other", Port: 3000},
			{Container: "app-new", Port: 3000},
		},
	}

	trimmed := withoutRoutesTo(spec, map[string]bool{"app-new": true})

	if len(trimmed.Routes) != 1 || trimmed.Routes[0].Container != "other" {
		t.Errorf("routes = %+v, want only the ones to a version that answered", trimmed.Routes)
	}
	// The specification held is not altered, since the version that failed is still wanted and
	// will be tried again.
	if len(spec.Routes) != 3 {
		t.Error("the specification the agent holds was altered")
	}
}

func TestRoutesAreUntouchedWhenEverythingAnswered(t *testing.T) {
	spec := &proto.Spec{Routes: []proto.SpecRoute{{Host: "app.example.com", Container: "app"}}}

	if trimmed := withoutRoutesTo(spec, nil); trimmed != spec {
		t.Error("the specification was copied when nothing had failed")
	}
}

// A version that was started and never answered is finished. Starting it again would begin the
// same wait for the same outcome, and hold up everything else on the machine while it did.
func TestAVersionGivenUpOnIsNotStartedAgain(t *testing.T) {
	spec := &proto.Spec{Containers: []proto.SpecContainer{deployed("app-broken", "two")}}

	plan := planRollout(map[string]proto.Container{}, spec, map[string]bool{"app-broken": true})

	if len(plan.create) != 0 {
		t.Errorf("create = %+v, want a version already given up on left alone", plan.create)
	}
}

// Giving up on one version says nothing about the next, which is a different container entirely.
func TestGivingUpOnAVersionDoesNotAffectTheNextOne(t *testing.T) {
	spec := &proto.Spec{Containers: []proto.SpecContainer{deployed("app-new", "three")}}

	plan := planRollout(map[string]proto.Container{}, spec, map[string]bool{"app-broken": true})

	if len(plan.create) != 1 {
		t.Errorf("create = %+v, want the new version started", plan.create)
	}
}
