package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Server-sent events are the push channel, for both the device and the console.
//
// The choice matters and is worth stating once. A WebSocket would carry the same traffic, but push
// here is one-way — the server tells a device that something changed, and the device answers over
// ordinary POSTs — so a socket buys a second protocol, a second framing bug surface, and a proxy
// that has to be configured to upgrade it. SSE is a plain GET that stays open.
//
// The property that makes this safe is that **an event is a wake-up, never the delivery itself**.
// A command is delivered when the device fetches it and the fetch is recorded; an event only says
// "there is something to fetch". So a dropped event, a proxy that buffered, a phone that was in a
// tunnel — none of them can lose a command. They cost latency until the next heartbeat, and
// nothing else. That is what keeps NFR-3 true: no path reports a delivery that did not happen.
const (
	// keepAlive is well under the 60 s idle timeout ingress controllers default to. A comment
	// frame is not an event; it exists so the connection is not reaped as idle.
	keepAlive = 20 * time.Second

	// streamMaxAge bounds how long one connection lives. Reconnecting costs one request and gives
	// back a re-authentication and a fresh goroutine; a connection that lives forever accumulates
	// the opposite of both.
	streamMaxAge = 15 * time.Minute

	// subscriberQueue is deliberately small. A full queue means this client already has unread
	// wake-ups, so one more tells it nothing it does not already know.
	subscriberQueue = 8
)

// Event is what a subscriber receives. It carries identifiers, never authoritative state: the
// receiver reads the state back from the API, which is the only place that records having done so.
type Event struct {
	Type     string `json:"type"`
	ChildID  string `json:"child_id,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
}

type subscriber struct {
	ch   chan Event
	done chan struct{}
}

// Hub is the registry of open streams. It is safe for concurrent use.
type Hub struct {
	mu      sync.Mutex
	devices map[uuid.UUID]map[*subscriber]struct{}
	parents map[*subscriber]struct{}
	closed  bool
	log     *slog.Logger
}

func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		devices: map[uuid.UUID]map[*subscriber]struct{}{},
		parents: map[*subscriber]struct{}{},
		log:     log,
	}
}

// SubscribeDevice registers a stream for one device and returns the function that unregisters it.
// The caller must defer the returned function, or the hub leaks a subscriber per connection.
func (h *Hub) SubscribeDevice(id uuid.UUID) (*subscriber, func()) {
	sub := newSubscriber()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(sub.done)
		return sub, func() {}
	}
	if h.devices[id] == nil {
		h.devices[id] = map[*subscriber]struct{}{}
	}
	h.devices[id][sub] = struct{}{}
	return sub, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.devices[id], sub)
		if len(h.devices[id]) == 0 {
			// Removing the empty map matters: a family that enrolls and retires devices over years
			// would otherwise accumulate one empty map per device that ever connected.
			delete(h.devices, id)
		}
	}
}

// SubscribeParents registers a console stream. Every parent sees every event, because every parent
// administers the whole family — there is no per-parent visibility rule to enforce here.
func (h *Hub) SubscribeParents() (*subscriber, func()) {
	sub := newSubscriber()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(sub.done)
		return sub, func() {}
	}
	h.parents[sub] = struct{}{}
	return sub, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.parents, sub)
	}
}

func newSubscriber() *subscriber {
	return &subscriber{ch: make(chan Event, subscriberQueue), done: make(chan struct{})}
}

// PublishDevice wakes every open stream of one device.
func (h *Hub) PublishDevice(id uuid.UUID, ev Event) {
	if ev.DeviceID == "" {
		ev.DeviceID = id.String()
	}
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.devices[id]))
	for sub := range h.devices[id] {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	h.deliver(subs, ev)
}

// PublishParents wakes every open console stream.
func (h *Hub) PublishParents(ev Event) {
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.parents))
	for sub := range h.parents {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	h.deliver(subs, ev)
}

// deliver never blocks. Publishing happens on the request goroutine of whichever parent made a
// change; a slow reader must not be able to hold that request open.
func (h *Hub) deliver(subs []*subscriber, ev Event) {
	for _, sub := range subs {
		select {
		case sub.ch <- ev:
		default:
			h.log.Debug("stream queue full, wake-up dropped", "type", ev.Type, "device", ev.DeviceID)
		}
	}
}

// DeviceListeners reports how many streams a device currently has open. It exists for the
// end-to-end suite, which otherwise has no way to know a subscription is in place before it
// publishes — and a test that publishes into an empty hub and then asserts nothing arrived would
// pass whether or not the hub works.
func (h *Hub) DeviceListeners(id uuid.UUID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.devices[id])
}

// Close ends every open stream. Handlers return, their connections close, and clients reconnect to
// whichever instance is serving next.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, subs := range h.devices {
		for sub := range subs {
			close(sub.done)
		}
	}
	for sub := range h.parents {
		close(sub.done)
	}
	h.devices = map[uuid.UUID]map[*subscriber]struct{}{}
	h.parents = map[*subscriber]struct{}{}
}

// ---- handlers -------------------------------------------------------------

func (s *Server) deviceStream(c *gin.Context) {
	dev := deviceOf(c)
	sub, unsubscribe := s.hub.SubscribeDevice(dev.ID)
	defer unsubscribe()
	s.stream(c, sub, Event{Type: "connected", DeviceID: dev.ID.String()})
}

func (s *Server) parentEvents(c *gin.Context) {
	sub, unsubscribe := s.hub.SubscribeParents()
	defer unsubscribe()
	s.stream(c, sub, Event{Type: "connected"})
}

// stream writes the SSE body until the client goes away, the hub closes, or the age cap fires.
func (s *Server) stream(c *gin.Context, sub *subscriber, hello Event) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		// Without flushing, every event would sit in a buffer until the connection closed — which
		// is indistinguishable from a hub that never publishes. Refusing is the honest answer.
		failWith(c, http.StatusInternalServerError, "internal", "this server cannot stream")
		return
	}

	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Tells nginx-family proxies not to buffer. Without it a proxy may hold every event until the
	// response ends, which for a stream is never.
	h.Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	write := func(ev Event) bool {
		payload, err := json.Marshal(ev)
		if err != nil {
			s.log.Error("event is not serialisable", "type", ev.Type, "error", err)
			return true
		}
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", ev.Type, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// The hello frame is what tells a client the stream is established rather than merely accepted,
	// and it is what makes the reconnect loop on the device observable in a test.
	if !write(hello) {
		return
	}

	ping := time.NewTicker(keepAlive)
	defer ping.Stop()
	deadline := time.NewTimer(streamMaxAge)
	defer deadline.Stop()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.done:
			return
		case <-deadline.C:
			write(Event{Type: "reconnect"})
			return
		case <-ping.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev := <-sub.ch:
			if !write(ev) {
				return
			}
		}
	}
}
