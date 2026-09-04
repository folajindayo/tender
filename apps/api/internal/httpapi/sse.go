package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// sse streams state changes to one user's open tabs.
//
// The recipient of a transfer is watching a clock, so pushing beats polling.
// A heartbeat keeps intermediary proxies from closing an idle connection.
func (a *API) sse(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(r.URL.Query().Get("userId"))
	if err != nil {
		http.Error(w, "userId query parameter is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	events, cancel := a.Hub.Subscribe(userID)
	defer cancel()

	fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case payload, open := <-events:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
