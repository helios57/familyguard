package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/auth"
	"github.com/helios57/familyguard/backend/internal/store"
)

// jsonDecoder is the one decoder configuration the API uses. DisallowUnknownFields is set here
// rather than per handler so that no endpoint can quietly accept a field it does not implement.
func jsonDecoder(r io.Reader) *json.Decoder {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	return dec
}

type googleLoginRequest struct {
	IDToken string `json:"id_token"`
}

type sessionResponse struct {
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expires_at"`
	Parent    store.Parent `json:"parent"`
}

// googleLogin exchanges a verified Google ID token for a session token.
//
// Authorization is membership: the address in the verified token must already be a parent of this
// family. There is no self-registration path, because the first thing a stranger with a Google
// account would otherwise be able to do is enrol a device.
//
// Every failure below answers with the same status and the same message. Distinguishing "not a
// parent here" from "bad token" would turn this endpoint into an oracle for which addresses belong
// to the family.
func (s *Server) googleLogin(c *gin.Context) {
	var req googleLoginRequest
	if !bindJSON(c, &req) {
		return
	}
	deny := func(reason string, err error) {
		s.log.Warn("parent sign-in refused", "reason", reason, "error", err,
			"request_id", RequestIDOf(c), "client", c.ClientIP())
		failWith(c, http.StatusUnauthorized, "unauthorized", "sign-in failed")
	}

	if req.IDToken == "" {
		deny("no id token", nil)
		return
	}
	// Verify owns every property of the token, including that the address is verified — it refuses
	// an unverified one outright. Re-checking claims.EmailVerified here would be a branch that can
	// never be taken, and a control that cannot fire is worse than no control: it reads like one.
	claims, err := s.verifier.Verify(c.Request.Context(), req.IDToken)
	if err != nil {
		deny("token rejected", err)
		return
	}

	session, err := s.issueSession(c, claims)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			deny("not a parent of this family", nil)
			return
		}
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// issueSession turns verified identity claims into a session.
//
// Both ways in — this JSON endpoint and the browser redirect flow — end here, so there is exactly
// one place that decides who becomes a session and what it records. A second copy of this logic is
// how one path ends up skipping the membership check.
func (s *Server) issueSession(c *gin.Context, claims *auth.IDTokenClaims) (*sessionResponse, error) {
	parent, err := s.store.ParentByEmail(c.Request.Context(), claims.Email)
	if err != nil {
		return nil, err
	}
	if err := s.store.RecordParentLogin(c.Request.Context(), parent.ID, claims.Subject, claims.Name); err != nil {
		return nil, err
	}
	token, expires, err := s.sessions.Issue(parent.ID, parent.Email, parent.Role, s.now())
	if err != nil {
		return nil, err
	}
	s.audit(c, store.ActorParent, parent.ID.String(), "PARENT_SIGNED_IN", "parent", parent.ID.String(), nil)
	parent.LastLoginAt = ptr(s.now())
	return &sessionResponse{Token: token, ExpiresAt: expires, Parent: *parent}, nil
}

// requireParent authenticates a parent, by session token or by API key.
//
// The identity is re-read against the database on every request rather than trusted from the
// credential. A session token is valid for a day and an API key until it is revoked; a parent
// removed from the family must lose access immediately, not when their credential happens to
// expire.
//
// **An API key resolves to a parent and then takes exactly the same path.** That is the whole
// design of FR-17, and it is why "every endpoint is reachable with a key" cost one branch rather
// than an endpoint-by-endpoint audit: the parent surface is a single gin group behind this
// function, so every requireRole gate, every handler, every audit entry and every rate-limit
// bucket downstream is unchanged and cannot be forgotten. A key carries no authority of its own —
// it is a way to be a parent, not a second authorization model, and the one that would eventually
// disagree with the first.
//
// What a key does NOT reach is deliberately small and lives outside this group: /auth/* (there is
// no session to establish — the key IS the credential) and /enroll (a device's one-time token).
func (s *Server) requireParent() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := auth.BearerToken(c.GetHeader("Authorization"))
		if token == "" {
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}

		var (
			parent *store.Parent
			err    error
			actor  string
		)
		if auth.IsAPIKey(token) {
			// Routed on the scheme prefix, not attempted-and-fallen-back-to. See auth.APIKeyScheme.
			parent, err = s.store.ParentByAPIKeyHash(c.Request.Context(), auth.HashToken(token), s.now())
			actor = ctxAPIKeyActor
		} else {
			var claims *auth.SessionClaims
			claims, err = s.sessions.Verify(token)
			if err == nil {
				parent, err = s.store.ParentByID(c.Request.Context(), claims.ParentID)
			}
		}
		if err != nil || parent == nil {
			// Including ErrNotFound: a deleted parent, or a revoked key, holding a still-valid
			// credential is exactly the case this re-read exists for. One message for both, because
			// distinguishing them would say which of the two a given string is.
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		c.Set(ctxParent, parent)
		if actor != "" {
			// Recorded so the audit trail can tell a script from a person. The key's own prefix is
			// not put here: it would then appear in every audit row, and the useful question — which
			// key — is answered by api_keys.last_used_at without spreading a credential fragment
			// through the log.
			c.Set(ctxActorKind, actor)
		}
		c.Next()
	}
}

// requireInteractiveParent refuses an API key on the routes that hand out CREDENTIALS (FR-17.2).
//
// The rest of the parent surface is deliberately key-reachable (see requireParent). These few are
// not, and the reason is containment rather than authority: an API key already acts as a
// PRIMARY_ADMIN when its creator is one, so nothing here is beyond it. But if a leaked key can mint
// a second key or add a parent, then revoking the leaked key does not end the access it granted —
// the attacker simply keeps the credential they made with it, and the one action a parent can take
// in an emergency stops working. Revocation has to be sufficient, so the set of things a key can
// use to outlive its own revocation must be empty.
//
// Rate-limited the same as everything else, and the message says what to do rather than just
// refusing: the caller is a script, and its author is the person who has to fix it.
func (s *Server) requireInteractiveParent() gin.HandlerFunc {
	return func(c *gin.Context) {
		if actorTypeOf(c) == ctxAPIKeyActor {
			abortWith(c, http.StatusForbidden, "api_key_forbidden",
				"an API key cannot create or revoke credentials. Sign in to the console for this.")
			return
		}
		c.Next()
	}
}

// requireRole gates an endpoint on the role stored in the database, not the one in the token: a
// demotion has to take effect on the next request.
func (s *Server) requireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		parent := parentOf(c)
		if parent == nil {
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		for _, r := range roles {
			if parent.Role == r {
				c.Next()
				return
			}
		}
		abortWith(c, http.StatusForbidden, "forbidden", "your role may not do that")
	}
}

// requireDevice authenticates an enrolled device by its bearer token.
func (s *Server) requireDevice() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := auth.BearerToken(c.GetHeader("Authorization"))
		if token == "" {
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		device, err := s.store.DeviceByTokenHash(c.Request.Context(), auth.HashToken(token))
		if err != nil {
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		c.Set(ctxDevice, device)
		c.Next()
	}
}

func parentOf(c *gin.Context) *store.Parent {
	v, _ := c.Get(ctxParent)
	p, _ := v.(*store.Parent)
	return p
}

func deviceOf(c *gin.Context) *store.Device {
	v, _ := c.Get(ctxDevice)
	d, _ := v.(*store.Device)
	return d
}

func (s *Server) me(c *gin.Context) {
	c.JSON(http.StatusOK, parentOf(c))
}

// audit writes one entry and never fails the request it describes.
//
// The trade-off is deliberate and worth stating: a mutation that succeeded and then failed to be
// recorded is still a mutation, and rolling it back because the log write failed would make the
// audit log a new way to break the product. The failure is logged at error level so it is not
// silent.
func (s *Server) audit(c *gin.Context, actorType, actorID, action, targetType, targetID string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["request_id"] = RequestIDOf(c)
	if err := s.store.Audit(c.Request.Context(), actorType, actorID, action, targetType, targetID, detail); err != nil {
		s.log.Error("audit write failed", "action", action, "target", targetID, "error", err,
			"request_id", RequestIDOf(c))
	}
}

// auditParent is the common case: an action taken by the signed-in parent.
//
// A request authenticated with an API key is recorded as the parent it acts as — the authority
// really is that parent's — with the actor TYPE saying it arrived without a person at a keyboard.
// Losing that distinction is how an audit log stops being able to answer "did I do that, or did the
// automation?", which is the first question after an unexpected change.
func (s *Server) auditParent(c *gin.Context, action, targetType, targetID string, detail map[string]any) {
	p := parentOf(c)
	id := ""
	if p != nil {
		id = p.ID.String()
	}
	s.audit(c, actorTypeOf(c), id, action, targetType, targetID, detail)
}

// actorTypeOf reports how the caller authenticated.
func actorTypeOf(c *gin.Context) string {
	if kind, ok := c.Get(ctxActorKind); ok {
		if s, _ := kind.(string); s != "" {
			return s
		}
	}
	return store.ActorParent
}

func ptr[T any](v T) *T { return &v }
