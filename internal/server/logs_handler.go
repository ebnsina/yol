package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ebnsina/yol/internal/httpx"
	"github.com/ebnsina/yol/internal/org"
)

const defaultTailLines = 200

// logs streams a container's output to the browser for as long as it is watching.
//
// Nothing is stored: this passes straight through from the agent. Watching costs only the
// connection, which is what makes it reasonable to leave open.
func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	m, session, ok := h.member(w, r)
	if !ok {
		return
	}
	if err := m.Role.Require(org.CanViewLogs); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	serverID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	// Confirms the server belongs to this organization before anything is streamed from it.
	if _, err := h.svc.Get(r.Context(), m, session.User.ID, serverID); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	conn, connected := h.hub.Get(serverID)
	if !connected {
		httpx.Fail(w, r, httpx.Conflict(
			"This server is not connected right now, so its logs cannot be shown."))
		return
	}

	container := r.PathValue("container")
	if container == "" {
		httpx.Fail(w, r, httpx.NotFound("container"))
		return
	}

	tailLines := defaultTailLines
	if raw := r.URL.Query().Get("tail"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			tailLines = parsed
		}
	}

	streamID, chunks, err := h.svc.TailContainer(r.Context(), conn, h.streams, container, tailLines)
	if err != nil {
		httpx.Fail(w, r, httpx.Conflict("We could not start reading logs from that container."))
		return
	}
	defer h.svc.StopTail(r.Context(), conn, h.streams, streamID)

	flusher, canStream := w.(http.Flusher)
	if !canStream {
		httpx.Fail(w, r, httpx.Internal(fmt.Errorf("server: response cannot be streamed")))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer would defeat the point of streaming.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return

		case chunk, open := <-chunks:
			if !open {
				return
			}
			encoded, err := json.Marshal(chunk)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
				return
			}
			flusher.Flush()

			if chunk.Done {
				return
			}
		}
	}
}
