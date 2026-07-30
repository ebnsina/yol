package server

import (
	"testing"

	"github.com/ebnsina/yol/internal/proto"
)

func TestStreamsDeliverToTheirWatcher(t *testing.T) {
	streams := NewStreams()
	id, chunks := streams.Open()
	defer streams.Close(id)

	streams.Deliver(proto.LogChunk{
		StreamID: id,
		Lines:    []proto.LogLine{{Text: "hello", Stream: "stdout"}},
	})

	select {
	case chunk := <-chunks:
		if len(chunk.Lines) != 1 || chunk.Lines[0].Text != "hello" {
			t.Errorf("got %+v", chunk)
		}
	default:
		t.Fatal("nothing arrived on the watcher's channel")
	}
}

// A chunk for a stream nobody is watching is ordinary: the agent may still be sending when
// the reader has gone.
func TestStreamsDropChunksForUnknownStreams(t *testing.T) {
	streams := NewStreams()
	streams.Deliver(proto.LogChunk{StreamID: "nobody-is-watching"})

	if streams.Count() != 0 {
		t.Error("delivering to an unknown stream registered something")
	}
}

func TestStreamsCloseReleasesTheWatcher(t *testing.T) {
	streams := NewStreams()
	id, chunks := streams.Open()

	if streams.Count() != 1 {
		t.Fatalf("count = %d, want 1", streams.Count())
	}
	streams.Close(id)

	if _, open := <-chunks; open {
		t.Error("the channel should be closed so the reader stops")
	}
	if streams.Count() != 0 {
		t.Errorf("count = %d after closing, want 0", streams.Count())
	}
}

// One reader falling behind must not stall the connection every other stream shares.
func TestSlowWatcherDoesNotBlockDelivery(t *testing.T) {
	streams := NewStreams()
	id, _ := streams.Open()
	defer streams.Close(id)

	// Far more than the buffer holds, and nothing is reading.
	for range 500 {
		streams.Deliver(proto.LogChunk{StreamID: id})
	}
	// Reaching here at all is the assertion: delivery never blocked.
}

func TestStreamIDsAreUnique(t *testing.T) {
	streams := NewStreams()
	seen := make(map[string]bool)

	for range 100 {
		id, _ := streams.Open()
		if seen[id] {
			t.Fatalf("stream id %q was issued twice", id)
		}
		seen[id] = true
	}
	if streams.Count() != 100 {
		t.Errorf("count = %d, want 100", streams.Count())
	}
}
