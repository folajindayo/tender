// Package stream pushes state changes to connected clients over SSE.
//
// Server-sent events rather than websockets: the traffic is one-directional and
// this keeps the deployment to a single HTTP service with no extra infrastructure.
package stream

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"
)

type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type subscriber struct {
	id uuid.UUID
	ch chan []byte
}

type Hub struct {
	mu   sync.RWMutex
	subs map[uuid.UUID]map[*subscriber]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[uuid.UUID]map[*subscriber]struct{})}
}

// Subscribe registers a listener for one user. The returned cancel function
// must be called when the connection closes.
func (h *Hub) Subscribe(userID uuid.UUID) (<-chan []byte, func()) {
	s := &subscriber{id: userID, ch: make(chan []byte, 16)}

	h.mu.Lock()
	if h.subs[userID] == nil {
		h.subs[userID] = make(map[*subscriber]struct{})
	}
	h.subs[userID][s] = struct{}{}
	h.mu.Unlock()

	return s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set, ok := h.subs[userID]; ok {
			delete(set, s)
			if len(set) == 0 {
				delete(h.subs, userID)
			}
		}
		close(s.ch)
	}
}

// Publish delivers an event to every connection for the given users. Slow
// consumers are skipped rather than allowed to block the caller.
func (h *Hub) Publish(event Event, userIDs ...uuid.UUID) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	seen := make(map[uuid.UUID]bool, len(userIDs))
	for _, id := range userIDs {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		for s := range h.subs[id] {
			select {
			case s.ch <- payload:
			default: // consumer is behind; drop rather than stall the request
			}
		}
	}
}
