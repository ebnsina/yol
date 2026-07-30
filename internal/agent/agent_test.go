package agent

import (
	"testing"

	"github.com/ebnsina/yol/internal/proto"
)

// Watch-only is a promise to the customer. The agent decides it, so a mistake in the control
// plane cannot cause a change on a server someone asked us only to watch.
func TestWatchOnlyRefusesChangesButAllowsReading(t *testing.T) {
	watching := &Agent{mode: proto.ModeWatch}

	for _, instruction := range []proto.Type{proto.TypeTailLogs, proto.TypeStopTail, proto.TypeSurvey} {
		if !watching.permits(instruction) {
			t.Errorf("%s was refused, though reading changes nothing", instruction)
		}
	}
	if watching.permits(proto.TypeApplySpec) {
		t.Error("a watched server accepted an instruction to change it")
	}
}

func TestManagedServerAcceptsChanges(t *testing.T) {
	managed := &Agent{mode: proto.ModeManaged}

	if !managed.permits(proto.TypeApplySpec) {
		t.Error("a managed server refused an instruction to change it")
	}
	if !managed.permits(proto.TypeTailLogs) {
		t.Error("a managed server refused to read logs")
	}
}

// An agent that has not yet been told its mode must not change anything, so that failing to
// learn the mode is safe rather than permissive.
func TestAgentStartsUnableToChangeAnything(t *testing.T) {
	fresh := &Agent{}

	if fresh.permits(proto.TypeApplySpec) {
		t.Error("an agent with no mode set accepted an instruction to change the server")
	}
}

func TestSplitTimestamp(t *testing.T) {
	at, text := splitTimestamp("2026-07-30T08:12:24.123456789Z hello world")
	if at.IsZero() {
		t.Error("the timestamp docker prefixes was not read")
	}
	if text != "hello world" {
		t.Errorf("text = %q, want the line without its timestamp", text)
	}

	// A line without a timestamp is kept whole rather than losing its first word.
	_, plain := splitTimestamp("no timestamp here")
	if plain != "no timestamp here" {
		t.Errorf("text = %q, want the line unchanged", plain)
	}
}

// A repeated request for the same stream must replace the old one rather than leaving two
// readers of the same container running.
func TestTailsReplaceRatherThanDouble(t *testing.T) {
	tails := newTails()

	firstStopped := false
	tails.add("stream-1", func() { firstStopped = true })
	tails.add("stream-1", func() {})

	if !firstStopped {
		t.Error("the earlier stream was left running")
	}
	if len(tails.running) != 1 {
		t.Errorf("%d streams running, want 1", len(tails.running))
	}
}

func TestTailsStopAllEndsEverything(t *testing.T) {
	tails := newTails()
	stopped := 0
	for _, id := range []string{"a", "b", "c"} {
		tails.add(id, func() { stopped++ })
	}

	tails.stopAll()

	if stopped != 3 {
		t.Errorf("%d streams stopped, want 3", stopped)
	}
	if len(tails.running) != 0 {
		t.Errorf("%d streams still registered", len(tails.running))
	}
}
