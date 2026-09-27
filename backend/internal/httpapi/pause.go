package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type pauseRequest struct {
	// A pointer, so a body that forgot the field is refused rather than read as "unpause".
	Paused *bool `json:"paused"`
}

// pauseChild pauses or unpauses a child's phones (FR-21.1): everything the child can open is
// suspended except the critical and always-usable packages — calls, messages, the family's
// messengers — until someone unpauses. A guardian may do it; it is one of the guardian window's
// actions (FR-20.1).
//
// It is a policy state, not a command, so it survives a reboot and an offline phone: the phone
// caches the input it was computed from and recomputes with the pause in it.
func (s *Server) pauseChild(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req pauseRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.Paused == nil {
		failWith(c, http.StatusBadRequest, "invalid_input", "paused must be true or false")
		return
	}
	parent := parentOf(c)
	pol, err := s.store.SetPaused(c.Request.Context(), childID, *req.Paused, parent.ID)
	if err != nil {
		s.fail(c, err)
		return
	}
	// Each action written out, not chosen into a variable: the audit test finds actions by reading
	// the call sites, and a variable is invisible to it.
	if pol.Paused {
		s.auditParent(c, "PROFILE_PAUSED", "child", childID.String(), map[string]any{"version": pol.Version})
	} else {
		s.auditParent(c, "PROFILE_UNPAUSED", "child", childID.String(), map[string]any{"version": pol.Version})
	}
	s.notifyChild(c, childID, "policy")
	c.JSON(http.StatusOK, gin.H{"paused": pol.Paused, "paused_at": pol.PausedAt, "version": pol.Version})
}
