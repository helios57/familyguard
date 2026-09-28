package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/store"
)

// Live mode (FR-27, the API is FR-27.3): a parent or guardian keeps a phone connected and reporting its position every
// few seconds for a while — a child walking home, a phone that has gone missing. Guardians may use
// it, because the walk home is theirs as often as a parent's; what a guardian may read is the
// positions of the session, never the history before it (GET /devices/:id/locations stays admins).

const (
	liveDefaultMinutes = 30
	liveMaxMinutes     = 120
)

type liveRequest struct {
	Minutes *int `json:"minutes"`
}

type liveView struct {
	Active    bool             `json:"active"`
	LiveUntil *time.Time       `json:"live_until"`
	LiveSince *time.Time       `json:"live_since"`
	Locations []store.Location `json:"locations"`
}

func (s *Server) liveOf(dev *store.Device) liveView {
	return liveView{
		Active:    dev.LiveUntil != nil && dev.LiveUntil.After(s.now()),
		LiveUntil: dev.LiveUntil,
		LiveSince: dev.LiveSince,
		Locations: []store.Location{},
	}
}

func (s *Server) startLive(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req liveRequest
	if !bindJSON(c, &req) {
		return
	}
	minutes := liveDefaultMinutes
	if req.Minutes != nil {
		minutes = *req.Minutes
	}
	if minutes < 1 || minutes > liveMaxMinutes {
		failWith(c, http.StatusBadRequest, "invalid_input", "minutes must be 1..120")
		return
	}
	dev, err := s.store.StartLive(c.Request.Context(), id, s.now().Add(time.Duration(minutes)*time.Minute))
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "LIVE_STARTED", "device", id.String(), map[string]any{"minutes": minutes})
	s.hub.PublishDevice(id, Event{Type: "live", ChildID: dev.ChildID.String()})
	s.hub.PublishParents(Event{Type: "state", DeviceID: id.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, s.liveOf(dev))
}

func (s *Server) stopLive(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	dev, err := s.store.StopLive(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "LIVE_STOPPED", "device", id.String(), nil)
	s.hub.PublishDevice(id, Event{Type: "live", ChildID: dev.ChildID.String()})
	s.hub.PublishParents(Event{Type: "state", DeviceID: id.String(), ChildID: dev.ChildID.String()})
	c.JSON(http.StatusOK, s.liveOf(dev))
}

// getLive is the session and the positions captured since it began, newest first.
func (s *Server) getLive(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	dev, err := s.store.GetDevice(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	view := s.liveOf(dev)
	if dev.LiveSince != nil {
		locs, err := s.store.LocationsSince(c.Request.Context(), id, *dev.LiveSince, queryInt(c, "limit", 100, 1, 1000))
		if err != nil {
			s.fail(c, err)
			return
		}
		view.Locations = locs
	}
	c.JSON(http.StatusOK, view)
}
