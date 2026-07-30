package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/ebnsina/yol/internal/proto"
)

// Log lines are batched rather than sent one at a time, because a chatty container would
// otherwise produce a message per line.
const (
	logBatchInterval = 250 * time.Millisecond
	logBatchSize     = 100
	maxTailLines     = 1000
)

// tailer streams one container's logs until it is told to stop or the container ends.
type tailer struct {
	streamID string
	cancel   context.CancelFunc
}

// tails tracks what is currently being streamed, so a stop request can find its stream.
type tails struct {
	mu      sync.Mutex
	running map[string]*tailer
}

func newTails() *tails {
	return &tails{running: make(map[string]*tailer)}
}

func (t *tails) add(streamID string, cancel context.CancelFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// A repeated request for the same stream replaces the old one rather than doubling it.
	if existing, ok := t.running[streamID]; ok {
		existing.cancel()
	}
	t.running[streamID] = &tailer{streamID: streamID, cancel: cancel}
}

func (t *tails) stop(streamID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if existing, ok := t.running[streamID]; ok {
		existing.cancel()
		delete(t.running, streamID)
	}
}

func (t *tails) stopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for id, existing := range t.running {
		existing.cancel()
		delete(t.running, id)
	}
}

// startTail begins streaming a container's logs. Reading logs is not changing anything, so it
// is allowed for any container, ours or the customer's, and in watch-only mode too.
func (a *Agent) startTail(ctx context.Context, req proto.TailLogs) {
	if req.StreamID == "" || req.Container == "" {
		return
	}

	tailCtx, cancel := context.WithCancel(ctx)
	a.tails.add(req.StreamID, cancel)

	go func() {
		defer a.tails.stop(req.StreamID)
		if err := a.streamLogs(tailCtx, req); err != nil && tailCtx.Err() == nil {
			slog.Debug("log stream ended", "container", req.Container, "error", err)
			a.sendLogChunk(ctx, proto.LogChunk{
				StreamID: req.StreamID,
				At:       time.Now().UTC(),
				Done:     true,
				Reason:   "We stopped receiving logs from this container.",
			})
			return
		}
		a.sendLogChunk(ctx, proto.LogChunk{StreamID: req.StreamID, At: time.Now().UTC(), Done: true})
	}()
}

// streamLogs runs docker logs and forwards what it produces.
func (a *Agent) streamLogs(ctx context.Context, req proto.TailLogs) error {
	tail := min(max(req.TailLines, 0), maxTailLines)

	// Arguments are passed separately rather than through a shell. The container name comes
	// from the control plane, and a name containing shell characters must never be able to
	// run anything on a customer's machine.
	args := []string{"logs", "--timestamps", "--tail", strconv.Itoa(tail)}
	if req.Follow {
		args = append(args, "--follow")
	}
	args = append(args, req.Container)

	cmd := exec.CommandContext(ctx, "docker", args...)
	if a.cfg.DockerHost != "" {
		cmd.Env = append(cmd.Environ(), "DOCKER_HOST="+a.cfg.DockerHost)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("agent: read log output: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("agent: read log output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agent: start reading logs: %w", err)
	}

	lines := make(chan proto.LogLine, 256)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); scanInto(stdout, "stdout", lines) }()
	go func() { defer readers.Done(); scanInto(stderr, "stderr", lines) }()
	go func() { readers.Wait(); close(lines) }()

	a.forwardLines(ctx, req.StreamID, lines)
	return cmd.Wait()
}

// scanInto reads lines and tags them with the stream they came from.
func scanInto(r io.Reader, stream string, out chan<- proto.LogLine) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		at, text := splitTimestamp(scanner.Text())
		out <- proto.LogLine{At: at, Stream: stream, Text: text}
	}
}

// splitTimestamp separates the timestamp docker prefixes from the line itself, so the
// interface can format it for the reader rather than showing a raw one.
func splitTimestamp(line string) (time.Time, string) {
	prefix, rest, found := cutSpace(line)
	if !found {
		return time.Now().UTC(), line
	}
	at, err := time.Parse(time.RFC3339Nano, prefix)
	if err != nil {
		return time.Now().UTC(), line
	}
	return at.UTC(), rest
}

func cutSpace(line string) (string, string, bool) {
	for i := range len(line) {
		if line[i] == ' ' {
			return line[:i], line[i+1:], true
		}
	}
	return "", line, false
}

// forwardLines batches lines and sends them, so a chatty container does not produce a message
// per line.
func (a *Agent) forwardLines(ctx context.Context, streamID string, lines <-chan proto.LogLine) {
	ticker := time.NewTicker(logBatchInterval)
	defer ticker.Stop()

	batch := make([]proto.LogLine, 0, logBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		a.sendLogChunk(ctx, proto.LogChunk{
			StreamID: streamID,
			At:       time.Now().UTC(),
			Lines:    batch,
		})
		batch = make([]proto.LogLine, 0, logBatchSize)
	}

	for {
		select {
		case <-ctx.Done():
			return

		case line, ok := <-lines:
			if !ok {
				flush()
				return
			}
			batch = append(batch, line)
			if len(batch) >= logBatchSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

func (a *Agent) sendLogChunk(ctx context.Context, chunk proto.LogChunk) {
	encoded, err := proto.Encode(proto.TypeLogChunk, chunk)
	if err != nil {
		return
	}
	if err := a.sendRaw(ctx, encoded); err != nil {
		slog.Debug("could not send log lines", "error", err)
	}
}
