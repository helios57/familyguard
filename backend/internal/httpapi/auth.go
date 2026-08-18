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

// requireParent authenticates a parent session.
//
// The claims are re-read against the database on every request rather than trusted from the token.
// A token is valid for a day; a parent removed from the family must lose access immediately, not
// when their token happens to expire.
func (s *Server) requireParent() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := auth.BearerToken(c.GetHeader("Authorization"))
		if token == "" {
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		claims, err := s.sessions.Verify(token)
		if err != nil {
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		parent, err := s.store.ParentByID(c.Request.Context(), claims.ParentID)
		if err != nil {
			// Including ErrNotFound: a deleted parent holding a still-valid token is exactly the
			// case this re-read exists for.
			abortWith(c, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		c.Set(ctxParent, parent)
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
func (s *Server) auditParent(c *gin.Context, action, targetType, targetID string, detail map[string]any) {
	p := parentOf(c)
	id := ""
	if p != nil {
		id = p.ID.String()
	}
	s.audit(c, store.ActorParent, id, action, targetType, targetID, detail)
}

func ptr[T any](v T) *T { return &v }
