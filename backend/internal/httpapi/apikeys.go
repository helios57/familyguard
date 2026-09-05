package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/auth"
)

// API keys (FR-17): a second way to present the SAME parent identity.
//
// A key is not a second kind of account and carries no scopes. It authenticates as the parent that
// created it and reaches exactly the routes that parent's browser session reaches, because the
// alternative — a permission model with two independent sources of truth — is the kind that grants
// something in one and not the other and is discovered by a caller who cannot do their job.
//
// What a key does NOT reach is /auth: it cannot mint a session, cannot change the parent's
// password, and cannot create more keys of its own. That is enforced in requireParent's caller
// graph rather than here — see the route table in server.go.

// createKeyRequest is the body of POST /api/v1/api-keys.
//
// There is no expiry field, and that is a decision rather than an omission (FR-17.3). The realistic holder of
// a key here is a long-running MCP server, so any expiry offered would be set to "never" — a field
// that does nothing — or set and forgotten, which is an outage whose cause is a date. Revocation is
// the control that matters, it is immediate, and `last_used_at` is what tells you which key to
// revoke.
type createKeyRequest struct {
	Name string `json:"name"`
}

// listAPIKeys returns every key in the family, not just the caller's.
//
// Nothing else in this API scopes a read to one parent — both parents administer the same children
// — and a key list is the worst possible place to start. "Which credentials can reach our family's
// data, and is one of them being used" is a question a co-parent has to be able to answer about a
// key they did not create.
func (s *Server) listAPIKeys(c *gin.Context) {
	keys, err := s.store.ListAPIKeys(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_keys": keys})
}

// createAPIKey mints one, and is the only time its token is ever readable (FR-17.1).
//
// Only the SHA-256 is stored, so this response cannot be reproduced — not by another request, not
// by an operator with database access. That is the point, and the console says so at the moment it
// shows the value rather than in documentation nobody reads at that moment.
func (s *Server) createAPIKey(c *gin.Context) {
	var req createKeyRequest
	if !bindJSON(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		failWith(c, http.StatusBadRequest, "invalid_input", "give the key a name so you can tell it apart later")
		return
	}

	token, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		s.fail(c, err)
		return
	}
	key, err := s.store.CreateAPIKey(c.Request.Context(), parentOf(c).ID, name, prefix, hash)
	if err != nil {
		s.fail(c, err)
		return
	}
	// Set on the returned struct only, never persisted and never logged. The audit entry below
	// records the prefix, which is what identifies the key in a log without being able to use it.
	key.Token = token
	s.auditParent(c, "API_KEY_CREATED", "api_key", key.ID.String(), map[string]any{
		"name": key.Name, "prefix": key.Prefix,
	})
	c.JSON(http.StatusCreated, key)
}

// revokeAPIKey stops a key working without losing the record that it existed.
//
// Revoking rather than deleting is the default action in the console because the audit log refers
// to keys by id: deleting the row would leave "API_KEY_CREATED … 3f2a" entries pointing at nothing,
// which is exactly the history you want when working out what a leaked key did.
func (s *Server) revokeAPIKey(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	key, err := s.store.RevokeAPIKey(c.Request.Context(), id, s.now())
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "API_KEY_REVOKED", "api_key", key.ID.String(), map[string]any{
		"name": key.Name, "prefix": key.Prefix,
	})
	c.JSON(http.StatusOK, key)
}

func (s *Server) deleteAPIKey(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	key, err := s.store.DeleteAPIKey(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "API_KEY_DELETED", "api_key", key.ID.String(), map[string]any{
		"name": key.Name, "prefix": key.Prefix,
	})
	c.Status(http.StatusNoContent)
}
