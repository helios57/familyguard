package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/push"
)

// pushCoalesce is the least time between two wake-ups of one phone. A policy change fans out one
// event per device and a burst of commands one each; the phone syncs everything in one go, so every
// push after the first in a burst would wake it for nothing.
const pushCoalesce = 10 * time.Second

// pushWake wakes a phone through FCM (FR-26.3): an event just reached none of its streams, or the
// phone reported a new push address and is waiting for proof that push reaches it. Off the caller's
// goroutine, because the caller is a request handler and FCM is a round trip to Google.
func (s *Server) pushWake(id uuid.UUID) {
	if s.push == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		token, err := s.store.PushToken(ctx, id)
		if err != nil || token == "" {
			return
		}
		// Counted from a push actually sent: an event for a phone with no address yet must not
		// swallow the proof push its first address is owed.
		s.pushMu.Lock()
		if last, ok := s.pushLast[id]; ok && s.now().Sub(last) < pushCoalesce {
			s.pushMu.Unlock()
			return
		}
		s.pushLast[id] = s.now()
		s.pushMu.Unlock()
		err = s.push.Tickle(ctx, token)
		switch {
		case err == nil:
			s.log.Info("push: woke a resting phone", "device", id)
		case errors.Is(err, push.ErrUnregistered):
			// The phone polls every 5 minutes until it reports a new token; keeping a dead one would
			// only make every event cost a failed round trip.
			if dropErr := s.store.DropPushToken(ctx, id, token); dropErr != nil {
				s.log.Error("push: could not drop a refused token", "device", id, "error", dropErr)
			}
			s.log.Warn("push: FCM refused the token; dropped it", "device", id, "error", err)
		default:
			s.log.Warn("push: not delivered; the phone hears of it at its next poll", "device", id, "error", err)
		}
	}()
}
