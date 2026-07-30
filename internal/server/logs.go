package server

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"strings"
	"sync"

	"github.com/ebnsina/yol/internal/proto"
)

// Streams routes log chunks arriving from agents to whoever is watching them.
//
// Nothing here is stored. Watching logs live costs only the connection it travels over, which
// is what keeps it free to leave open.
type Streams struct {
	mu       sync.RWMutex
	watchers map[string]chan proto.LogChunk
}

// NewStreams builds an empty registry.
func NewStreams() *Streams {
	return &Streams{watchers: make(map[string]chan proto.LogChunk)}
}

// Open registers a watcher and returns the channel its chunks arrive on, along with the
// identifier to ask the agent to stream against.
func (s *Streams) Open() (string, <-chan proto.LogChunk) {
	id := newStreamID()
	// Buffered so a slow reader delays only itself; chunks are dropped rather than blocking
	// the connection every other stream shares.
	ch := make(chan proto.LogChunk, 64)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchers[id] = ch
	return id, ch
}

// Close ends a stream and releases it.
func (s *Streams) Close(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ch, ok := s.watchers[id]; ok {
		close(ch)
		delete(s.watchers, id)
	}
}

// Deliver passes a chunk to its watcher. A chunk for an unknown stream is dropped, which is
// ordinary: the agent may still be sending when the reader has gone.
func (s *Streams) Deliver(chunk proto.LogChunk) {
	s.mu.RLock()
	ch, ok := s.watchers[chunk.StreamID]
	s.mu.RUnlock()

	if !ok {
		return
	}

	select {
	case ch <- chunk:
	default:
		// The reader is not keeping up. Dropping is better than stalling every other stream.
	}
}

// Count reports how many streams are open.
func (s *Streams) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.watchers)
}

func newStreamID() string {
	var raw [10]byte
	rand.Read(raw[:])
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
}

// TailContainer asks a connected agent to stream a container's logs, and stops it when the
// caller goes away. Reading logs changes nothing, so it is allowed for any container on the
// machine, ours or the customer's, and on a watched server too.
func (s *Service) TailContainer(
	ctx context.Context,
	conn *Connection,
	streams *Streams,
	container string,
	tailLines int,
) (string, <-chan proto.LogChunk, error) {
	streamID, chunks := streams.Open()

	err := conn.Send(ctx, proto.TypeTailLogs, proto.TailLogs{
		StreamID:  streamID,
		Container: container,
		TailLines: tailLines,
		Follow:    true,
	})
	if err != nil {
		streams.Close(streamID)
		return "", nil, err
	}
	return streamID, chunks, nil
}

// StopTail tells the agent to stop streaming.
func (s *Service) StopTail(ctx context.Context, conn *Connection, streams *Streams, streamID string) {
	_ = conn.Send(ctx, proto.TypeStopTail, proto.StopTail{StreamID: streamID})
	streams.Close(streamID)
}
