package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/store"
	"github.com/helios57/familyguard/backend/internal/webpush"
)

// Bounds of one request (FR-28.1). The phone offers 15, 30 and 60; a parent may give anything up to
// the ceiling, more or less than was asked.
const (
	minRequestMinutes = 5
	maxRequestMinutes = 240
	maxRequestNote    = 140
)

type timeRequestBody struct {
	Minutes int    `json:"minutes"`
	Note    string `json:"note"`
}

// deviceRequestTime is the child's "Mehr Zeit erbitten" (FR-28.1). It changes nothing by itself: it
// asks every parent, and only a parent's answer gives time.
func (s *Server) deviceRequestTime(c *gin.Context) {
	dev := deviceOf(c)
	var req timeRequestBody
	if !bindJSON(c, &req) {
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Minutes < minRequestMinutes || req.Minutes > maxRequestMinutes {
		failWith(c, http.StatusBadRequest, "invalid_input",
			fmt.Sprintf("minutes must be between %d and %d", minRequestMinutes, maxRequestMinutes))
		return
	}
	if utf8.RuneCountInString(req.Note) > maxRequestNote {
		failWith(c, http.StatusBadRequest, "invalid_input", fmt.Sprintf("the note is at most %d characters", maxRequestNote))
		return
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	if pol.DailyLimitMinutes <= 0 {
		failWith(c, http.StatusConflict, "no_daily_limit", "this profile has no daily limit, so there is no time to ask for")
		return
	}
	day, err := enforce.DayKey(pol, s.now())
	if err != nil {
		s.fail(c, err)
		return
	}
	r, err := s.store.CreateTimeRequest(ctx, dev.ChildID, dev.ID, day, req.Minutes, req.Note)
	switch {
	case errors.Is(err, store.ErrTimeRequestOpen):
		failWith(c, http.StatusConflict, "already_asked", "a request for today is still waiting for an answer")
		return
	case errors.Is(err, store.ErrTimeRequestsExhausted):
		failWith(c, http.StatusConflict, "no_requests_left",
			fmt.Sprintf("at most %d requests a day", store.MaxTimeRequestsPerDay))
		return
	case err != nil:
		s.fail(c, err)
		return
	}
	s.audit(c, store.ActorDevice, dev.ID.String(), "TIME_REQUESTED", "child", dev.ChildID.String(),
		map[string]any{"request": r.ID.String(), "minutes": r.Minutes, "day": day})
	s.hub.PublishParents(Event{Type: "time_request", ChildID: dev.ChildID.String(), DeviceID: dev.ID.String()})
	body := r.Note
	if body == "" {
		body = "Tippe, um zu antworten."
	}
	s.tellParents(dev.ChildID, func(name string) webpush.Message {
		return webpush.Message{
			Title: fmt.Sprintf("%s bittet um %d Min. mehr", name, r.Minutes),
			Body:  body, Tag: "time-" + dev.ChildID.String(), URL: "#/",
		}
	})
	view, err := s.today(ctx, dev.ChildID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

type timeDecisionBody struct {
	// Decision is grant or decline.
	Decision string `json:"decision"`
	// Minutes is what a grant gives; 0 or absent gives what was asked.
	Minutes int `json:"minutes"`
}

// decideTimeRequest answers today's request (FR-28.2). A grant is today's extra time — it ends at
// midnight like the console's +15 — written in the same transaction as the answer.
func (s *Server) decideTimeRequest(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	requestID, ok := uuidParam(c, "request")
	if !ok {
		return
	}
	var req timeDecisionBody
	if !bindJSON(c, &req) {
		return
	}
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	if decision != "grant" && decision != "decline" {
		failWith(c, http.StatusBadRequest, "invalid_input", "decision must be grant or decline")
		return
	}
	if req.Minutes < 0 || req.Minutes > maxRequestMinutes || (decision == "decline" && req.Minutes != 0) {
		failWith(c, http.StatusBadRequest, "invalid_input",
			fmt.Sprintf("minutes is 1 to %d for a grant, and absent for a decline", maxRequestMinutes))
		return
	}
	ctx := c.Request.Context()
	pol, err := s.store.GetPolicy(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	day, err := enforce.DayKey(pol, s.now())
	if err != nil {
		s.fail(c, err)
		return
	}
	grant := decision == "grant"
	if grant && pol.DailyLimitMinutes <= 0 {
		failWith(c, http.StatusConflict, "no_daily_limit",
			"this child has no daily limit any more, so there is no time to add")
		return
	}
	r, total, err := s.store.DecideTimeRequest(ctx, childID, requestID, day, grant, req.Minutes, parentOf(c).ID)
	switch {
	case errors.Is(err, store.ErrTimeRequestNotFound):
		failWith(c, http.StatusNotFound, "not_found", "that request is not one of this profile's")
		return
	case errors.Is(err, store.ErrTimeRequestDecided):
		failWith(c, http.StatusConflict, "already_decided", "that request was already answered, or its day is over")
		return
	case errors.Is(err, store.ErrBonusTooLarge):
		failWith(c, http.StatusConflict, "bonus_too_large", "a day cannot have more than 1440 minutes of extra time")
		return
	case err != nil:
		s.fail(c, err)
		return
	}
	detail := map[string]any{"request": requestID.String(), "day": day, "asked": r.Minutes}
	if r.State == store.TimeRequestGranted {
		if err := s.recordDayLimits(ctx, childID, pol, day); err != nil {
			s.fail(c, err)
			return
		}
		detail["minutes"] = r.GrantedMinutes
		detail["bonus_minutes"] = total
		s.auditParent(c, "TIME_REQUEST_GRANTED", "child", childID.String(), detail)
	} else {
		s.auditParent(c, "TIME_REQUEST_DECLINED", "child", childID.String(), detail)
	}
	// The phones fetch the answer (and the time); the other parents' windows drop the request.
	s.notifyChild(c, childID, "policy")
	view, err := s.today(ctx, childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"today": view, "request": r})
}

// tellParents sends a Web Push to every subscribed parent browser, off the request goroutine: the
// push services are a round trip away and the child's phone is waiting for its answer.
func (s *Server) tellParents(childID uuid.UUID, build func(childName string) webpush.Message) {
	if s.webPush == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		child, err := s.store.GetChild(ctx, childID)
		if err != nil {
			s.log.Warn("webpush: no child to name", "child", childID, "error", err)
			return
		}
		if _, err := s.webPush.Notify(ctx, build(child.Name)); err != nil {
			s.log.Warn("webpush: not sent", "error", err)
		}
	}()
}

// ---- the parent's browser: subscribing ------------------------------------------------------

func (s *Server) webPushKey(c *gin.Context) {
	key, err := s.webPush.PublicKey(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"public_key": key})
}

type webPushSubscriptionBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Server) putWebPushSubscription(c *gin.Context) {
	var req webPushSubscriptionBody
	if !bindJSON(c, &req) {
		return
	}
	if !s.webPush.AllowedEndpoint(req.Endpoint) || len(req.Endpoint) > 2048 {
		failWith(c, http.StatusBadRequest, "invalid_input", "the endpoint is not a browser push service")
		return
	}
	if req.Keys.P256dh == "" || len(req.Keys.P256dh) > 256 || req.Keys.Auth == "" || len(req.Keys.Auth) > 64 {
		failWith(c, http.StatusBadRequest, "invalid_input", "keys.p256dh and keys.auth are required")
		return
	}
	if err := s.store.SaveWebPushSubscription(c.Request.Context(), parentOf(c).ID, req.Endpoint, req.Keys.P256dh, req.Keys.Auth); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscribed": true})
}

type webPushEndpointBody struct {
	Endpoint string `json:"endpoint"`
}

// webPushSubscriptionStatus says whether this browser's subscription is held for the caller. A
// POST, because the endpoint is a capability URL and does not belong in an access log.
func (s *Server) webPushSubscriptionStatus(c *gin.Context) {
	var req webPushEndpointBody
	if !bindJSON(c, &req) {
		return
	}
	ok, err := s.store.HasWebPushSubscription(c.Request.Context(), parentOf(c).ID, req.Endpoint)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscribed": ok})
}

func (s *Server) deleteWebPushSubscription(c *gin.Context) {
	var req webPushEndpointBody
	if !bindJSON(c, &req) {
		return
	}
	if err := s.store.DeleteWebPushSubscription(c.Request.Context(), parentOf(c).ID, req.Endpoint); err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"subscribed": false})
}
