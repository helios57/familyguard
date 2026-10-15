package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/store"
)

// Sign-ins (FR-29): a parent stays signed in while they use the console, and every sign-in can be
// seen and ended from any other.
//
// The list is the reason a sign-in that lasts for months is acceptable. A credential that outlives
// the session it was minted for is only as safe as the ability to find it and end it, and "I lost
// my phone" has to be answerable from the laptop, by either parent, without a support request.

func signInOf(c *gin.Context) uuid.UUID {
	v, _ := c.Get(ctxSignIn)
	id, _ := v.(uuid.UUID)
	return id
}

// listSignIns returns every live sign-in of the family and marks the caller's own (FR-29.4). An API
// key has no sign-in, so its list marks none — which is the truth, not an omission.
func (s *Server) listSignIns(c *gin.Context) {
	list, err := s.store.ListSignIns(c.Request.Context(), s.now().Add(-s.cfg.SignInIdleTimeout))
	if err != nil {
		s.fail(c, err)
		return
	}
	own := signInOf(c)
	for i := range list {
		list[i].Current = own != uuid.Nil && list[i].ID == own
	}
	c.JSON(http.StatusOK, gin.H{"sign_ins": list})
}

// endCurrentSignIn is the console's Abmelden: the token stops working on the server, not merely in
// this browser's storage. A copy of it taken before the click is refused after it (FR-29.3).
func (s *Server) endCurrentSignIn(c *gin.Context) {
	id := signInOf(c)
	if id == uuid.Nil {
		// Unreachable through the route table — an API key is refused before this handler — and
		// answered as "no such record" rather than trusted if that ever changes.
		failWith(c, http.StatusNotFound, "not_found", "this request did not arrive with a sign-in")
		return
	}
	s.endSignInRow(c, id)
}

// endSignIn ends one sign-in by id. A parent may end their own on any device; ending another
// parent's is the primary admin's, the same bar as removing that parent outright.
func (s *Server) endSignIn(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	s.endSignInRow(c, id)
}

func (s *Server) endSignInRow(c *gin.Context, id uuid.UUID) {
	ctx := c.Request.Context()
	now := s.now()
	cutoff := now.Add(-s.cfg.SignInIdleTimeout)
	si, err := s.store.SignInByID(ctx, id, cutoff)
	if err != nil {
		s.fail(c, err)
		return
	}
	caller := parentOf(c)
	self := si.ParentID == caller.ID
	if !self && caller.Role != store.RolePrimaryAdmin {
		failWith(c, http.StatusForbidden, "forbidden", "only the primary admin can sign another parent out")
		return
	}
	if err := s.store.EndSignIn(ctx, id, now, cutoff); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "SIGN_IN_ENDED", "sign_in", id.String(), map[string]any{
		"parent_id": si.ParentID.String(), "label": si.Label, "own": self,
		"current": id == signInOf(c),
	})
	c.Status(http.StatusNoContent)
}

// signInLabel names a sign-in after the browser and system it was made from, so a parent can tell
// "Chrome · Android" from "Safari · iPhone" in the list. It is a label and nothing more: a
// User-Agent is whatever the client says, so nothing is ever decided on it.
func signInLabel(ua string) string {
	browser := ""
	for _, b := range []struct{ token, name string }{
		// Order matters: Edge and Samsung Internet also say "Chrome", and Chrome also says "Safari".
		{"Edg/", "Edge"}, {"EdgA/", "Edge"}, {"SamsungBrowser/", "Samsung Internet"},
		{"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"}, {"CriOS/", "Chrome"},
		{"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	system := ""
	for _, o := range []struct{ token, name string }{
		{"Android", "Android"}, {"iPhone", "iPhone"}, {"iPad", "iPad"}, {"CrOS", "ChromeOS"},
		{"Windows", "Windows"}, {"Mac OS X", "macOS"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, o.token) {
			system = o.name
			break
		}
	}
	switch {
	case browser != "" && system != "":
		return browser + " · " + system
	case browser != "":
		return browser
	case system != "":
		return system
	}
	// Not a browser: a script signing in with the JSON endpoint. Its product token is the best
	// name it has ("curl", "Go-http-client"), bounded so a hostile header cannot fill the list.
	product, _, _ := strings.Cut(strings.TrimSpace(ua), "/")
	product = strings.TrimSpace(product)
	if product == "" {
		return "Unbekanntes Gerät"
	}
	if r := []rune(product); len(r) > 40 {
		product = string(r[:40])
	}
	return product
}
