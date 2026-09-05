package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/auth"
	"github.com/helios57/familyguard/backend/internal/catalog"
	"github.com/helios57/familyguard/backend/internal/config"
	"github.com/helios57/familyguard/backend/internal/enforce"
	"github.com/helios57/familyguard/backend/internal/policy"
	"github.com/helios57/familyguard/backend/internal/provisioning"
	"github.com/helios57/familyguard/backend/internal/store"
)

// Deps is everything the server needs from outside. Constructing it explicitly, rather than having
// the server build its own dependencies, is what lets the end-to-end suite point the same code at
// a local issuer and a throwaway database without a single test-only branch inside a handler.
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Verifier *auth.OIDCVerifier
	Sessions *auth.SessionIssuer
	Logger   *slog.Logger

	// HTTPClient is used for outbound calls to the identity provider (the code exchange). Injected
	// so the end-to-end suite can point it at a local issuer without a global transport swap.
	HTTPClient *http.Client

	// Now is the clock. Injected so that a test can ask what applies at 21:00 without waiting for
	// 21:00, and so no handler reaches for time.Now behind the test's back.
	Now func() time.Time

	// APKChecksums are computed from the APK on disk at startup. Empty means provisioning QR codes
	// cannot be issued, which is reported as an error at the endpoint rather than as a QR the
	// device will reject halfway through setup.
	SignatureChecksum string
	PackageChecksum   string
}

// Server holds the wired HTTP surface.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	verifier *auth.OIDCVerifier
	sessions *auth.SessionIssuer
	resolver *enforce.Resolver
	catalog  *catalog.Catalog
	hub      *Hub
	log      *slog.Logger
	now      func() time.Time

	httpClient *http.Client

	signatureChecksum string
	packageChecksum   string
}

// New wires a server. It does not listen; the caller owns the lifecycle.
func New(d Deps) (*Server, error) {
	if d.Config == nil || d.Store == nil || d.Sessions == nil {
		return nil, errors.New("httpapi: config, store and sessions are required")
	}
	if d.Verifier == nil {
		// A nil verifier would make every parent login fail closed, which is safe but silent.
		// Refusing to start says so once, at the point where it can still be fixed.
		return nil, errors.New("httpapi: an OIDC verifier is required; parent sign-in is the only way in")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Server{
		cfg:               d.Config,
		store:             d.Store,
		verifier:          d.Verifier,
		sessions:          d.Sessions,
		resolver:          enforce.New(d.Store, d.Config.PublicURL.String()),
		catalog:           catalog.New(d.Config.APKDir, d.Store, d.Logger, d.Config.DPCPackage()),
		hub:               NewHub(d.Logger),
		log:               d.Logger,
		now:               d.Now,
		httpClient:        d.HTTPClient,
		signatureChecksum: d.SignatureChecksum,
		packageChecksum:   d.PackageChecksum,
	}, nil
}

// Catalog exposes the app catalog so the caller can scan the directory at startup. Doing it there
// rather than in New keeps the scan — which reads every APK on the node — out of the path of every
// test that constructs a server.
func (s *Server) Catalog() *catalog.Catalog { return s.catalog }

// Hub exposes the event hub so the caller can shut it down with the server.
func (s *Server) Hub() *Hub { return s.hub }

// Router builds the gin engine.
//
// The middleware order is deliberate and each step depends on the one before it: a request gets an
// id first so every later failure can be traced, then security headers so even a rejected request
// carries them, then the body cap before anything reads a body, then rate limiting before any
// database work, and only then routing.
func (s *Server) Router() (*gin.Engine, error) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// Trusted proxies are explicit. With none set, gin takes the socket peer as the client
	// address, so a forged X-Forwarded-For cannot pick its own rate-limit bucket or poison an
	// audit entry.
	if err := r.SetTrustedProxies(s.cfg.TrustedProxies); err != nil {
		return nil, err
	}

	limiter := NewRateLimiter(s.cfg.RateLimitPerMinute, DefaultRateLimitKeys)
	r.Use(
		gin.Recovery(),
		RequestID(),
		SecurityHeaders(),
		BodyLimit(s.cfg.MaxBodyBytes, map[string]int64{uploadAppRoute: s.cfg.MaxUploadBytes}),
		CORS(s.cfg.AllowedOrigins),
		RateLimit(limiter),
		s.accessLog(),
	)

	r.NoRoute(func(c *gin.Context) {
		failWith(c, http.StatusNotFound, "not_found", "no such endpoint")
	})

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/readyz", s.ready)

	// The DPC download, outside /api/v1 and outside every auth group — see serveAPK for why it
	// cannot have a credential, and why that is safe.
	r.GET(APKDownloadPath, s.serveAPK)

	v1 := r.Group("/api/v1")
	// Two ways to a session, one decision. POST /auth/google takes an ID token from a client that
	// already has one; the redirect pair below is how a browser gets one without this origin ever
	// loading the provider's script. Both end in issueSession.
	v1.POST("/auth/google", s.googleLogin)
	v1.GET("/auth/google/start", s.oauthStart)
	v1.GET("/auth/google/callback", s.oauthCallback)
	v1.POST("/enroll", s.enroll)

	// Parent surface.
	p := v1.Group("", s.requireParent())
	p.GET("/me", s.me)
	p.GET("/family", s.getFamily)
	p.GET("/parents", s.listParents)
	// requireInteractiveParent, here and on /api-keys below: these are the routes that hand out or
	// take away a credential, and an API key that can mint one outlives its own revocation.
	p.POST("/parents", s.requireInteractiveParent(), s.requireRole(store.RolePrimaryAdmin), s.createParent)
	p.DELETE("/parents/:id", s.requireInteractiveParent(), s.requireRole(store.RolePrimaryAdmin), s.deleteParent)

	p.GET("/children", s.listChildren)
	p.POST("/children", s.createChild)
	p.PATCH("/children/:id", s.updateChild)
	p.DELETE("/children/:id", s.deleteChild)
	p.GET("/children/:id/policy", s.getPolicy)
	p.PATCH("/children/:id/policy", s.patchPolicy)
	p.GET("/children/:id/app-rules", s.listAppRules)
	p.PUT("/children/:id/app-rules", s.putAppRule)
	p.DELETE("/children/:id/app-rules", s.deleteAppRule)
	p.GET("/children/:id/blocked-domains", s.listBlockedDomains)
	p.POST("/children/:id/blocked-domains", s.addBlockedDomain)
	p.DELETE("/children/:id/blocked-domains", s.removeBlockedDomain)
	p.POST("/children/:id/devices", s.createDevice)

	p.GET("/devices", s.listDevices)
	p.GET("/devices/:id", s.getDevice)
	p.PATCH("/devices/:id", s.renameDevice)
	p.DELETE("/devices/:id", s.deleteDevice)
	// Minting an enrollment credential is a state change, so it is a POST. There is deliberately no
	// GET that returns a QR: the plaintext token is not stored, so a read could only ever return a
	// QR that does not work.
	p.POST("/devices/:id/provisioning", s.provisioningPayload)
	p.GET("/devices/:id/recovery-code", s.recoveryCode)
	p.GET("/devices/:id/recovery-events", s.listRecoveryEvents)
	p.GET("/devices/:id/apps", s.listDeviceApps)
	p.GET("/devices/:id/usage", s.deviceUsage)
	p.GET("/devices/:id/locations", s.deviceLocations)
	p.GET("/devices/:id/desired-state", s.deviceDesiredState)
	p.GET("/devices/:id/commands", s.listCommands)
	p.POST("/devices/:id/commands", s.createCommand)
	// The application catalog (FR-16). Uploading is an admin action: a package that lands here can
	// be declared for a child and will install itself on their phone without anyone tapping
	// anything, which is a larger authority than editing a bedtime.
	p.GET("/apps", s.listApps)
	p.POST("/apps", s.requireRole(store.RolePrimaryAdmin, store.RoleAdmin), s.uploadApp)
	p.POST("/apps/scan", s.requireRole(store.RolePrimaryAdmin, store.RoleAdmin), s.scanApps)
	p.DELETE("/apps/:id", s.requireRole(store.RolePrimaryAdmin, store.RoleAdmin), s.deleteApp)
	p.GET("/children/:id/managed-apps", s.listManagedApps)
	p.PUT("/children/:id/managed-apps/:package", s.declareManagedApp)
	p.DELETE("/children/:id/managed-apps/:package", s.withdrawManagedApp)

	// API keys (FR-17). Minting one hands out this family's whole parent surface, so it is
	// PRIMARY_ADMIN only — the same bar as adding a parent, which is what it amounts to.
	//
	// Listing stays key-reachable: reading which credentials exist is not one of them, and an MCP
	// server that can answer "what keys does this family have" is useful. Everything that changes
	// the set is console-only.
	k := p.Group("/api-keys", s.requireRole(store.RolePrimaryAdmin))
	k.GET("", s.listAPIKeys)
	k.POST("", s.requireInteractiveParent(), s.createAPIKey)
	k.POST("/:id/revoke", s.requireInteractiveParent(), s.revokeAPIKey)
	k.DELETE("/:id", s.requireInteractiveParent(), s.deleteAPIKey)

	p.GET("/audit", s.listAudit)
	// The console reads this stream with fetch() rather than EventSource, because EventSource
	// cannot carry an Authorization header — and the alternatives are a cookie (which brings CSRF
	// with it) or a token in the query string (which lands in every access log in the path).
	p.GET("/events", s.parentEvents)

	// Device surface. Every route here authenticates with the device token issued at enrollment.
	d := v1.Group("/device", s.requireDevice())
	d.POST("/heartbeat", s.heartbeat)
	d.GET("/policy", s.devicePolicy)
	// Fetching commands is what records their delivery. The stream only says "there is something to
	// fetch", so a wake-up that never arrives costs latency and never a fabricated delivery (NFR-3).
	d.GET("/commands", s.deviceCommands)
	d.POST("/inventory", s.deviceInventory)
	d.POST("/usage", s.deviceUsageReport)
	d.POST("/location", s.deviceLocationReport)
	d.POST("/commands/:id/ack", s.ackCommand)
	d.POST("/recovery-event", s.recoveryEventReport)
	d.GET("/stream", s.deviceStream)
	// What DPC this server hosts, so the phone can tell whether the one it is running is the one it
	// should be (FR-15.1). Read-only and cheap; the download itself is unauthenticated, below.
	d.GET("/apk-info", s.apkInfo)
	// A managed application's bytes, addressed by the exact version the policy named. Device
	// authenticated, unlike /dpc.apk: a phone asking for one of these has been enrolled for weeks,
	// and there is no provisioning wizard here with nothing to present.
	d.GET("/apps/:package/:versionCode", s.deviceDownloadApp)

	if err := s.mountConsole(r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Server) ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Pool().Ping(ctx); err != nil {
		// Readiness reports the dependency it actually checked. "ok" from a probe that pinged
		// nothing is the failure mode this whole codebase is written against.
		failWith(c, http.StatusServiceUnavailable, "not_ready", "database unreachable")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "database": "ok"})
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := s.now()
		c.Next()
		s.log.Info("request",
			"method", c.Request.Method,
			"path", loggedPath(c),
			"status", c.Writer.Status(),
			"duration_ms", s.now().Sub(start).Milliseconds(),
			"request_id", RequestIDOf(c),
			"client", c.ClientIP())
	}
}

// loggedPath is what the access log records as "path".
//
// For a request that matched a route it is the route TEMPLATE — `/api/v1/devices/:id`, not the id
// — so the log has bounded cardinality and cannot carry a device id, an enrollment token or an
// email that arrived as a path segment.
//
// For a request that matched nothing, `c.FullPath()` is the empty string, and an empty field is
// the one thing a 404 line must not say: that line exists to answer "what 404'd", and this
// deployment is on the public internet, where most 404s are scanners and some are a broken link in
// our own console. Telling those apart is the entire value of the line. Found in production on
// 2026-08-18, in the first minutes of uptime: eight consecutive `"path":""` 404s from one address,
// carrying no information whatsoever.
//
// So unmatched requests fall back to the request path, with three deliberate limits:
//
//   - the query string is dropped, because that is where tokens and codes live;
//   - the result is truncated, because a raw path is attacker-controlled and unbounded;
//   - control characters are replaced, so a crafted path cannot rearrange the log even if some
//     downstream reader is less careful about escaping than slog's JSON handler is.
func loggedPath(c *gin.Context) string {
	if p := c.FullPath(); p != "" {
		return p
	}
	const maxLen = 100
	raw := c.Request.URL.Path
	var b strings.Builder
	for _, r := range raw {
		if b.Len() >= maxLen {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7f {
			b.WriteRune('?')
			continue
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		// A request line of "GET  HTTP/1.1" reaches here with an empty path. Say so, rather than
		// emit the empty string this function exists to eliminate.
		return "(empty)"
	}
	return b.String()
}

// ---- shared helpers -------------------------------------------------------

// fail maps a domain error onto a status. It is the only place that decides, so a handler cannot
// accidentally turn a missing row into a 500 or a validation failure into a 404.
func (s *Server) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		failWith(c, http.StatusNotFound, "not_found", "no such record")
	case errors.Is(err, store.ErrConflict):
		failWith(c, http.StatusConflict, "conflict", "that record already exists")
	case errors.Is(err, policy.ErrInvalidInput):
		failWith(c, http.StatusBadRequest, "invalid_input", err.Error())
	case errors.Is(err, provisioning.ErrInvalidParams):
		// Nothing here came from the request: this is the server's own configuration.
		s.log.Error("provisioning misconfigured", "error", err, "request_id", RequestIDOf(c))
		failWith(c, http.StatusInternalServerError, "misconfigured",
			"provisioning is not configured on this server")
	default:
		s.log.Error("request failed", "error", err, "path", c.FullPath(), "request_id", RequestIDOf(c))
		failWith(c, http.StatusInternalServerError, "internal", "something went wrong")
	}
}

// bindJSON decodes a request body and rejects unknown fields.
//
// Rejecting unknowns is the point. A console that PATCHes {"bedtime_star": "21:00"} would
// otherwise get 200 and no change: the parent sees success, the child's phone does not change,
// and nothing anywhere records that a field was dropped.
// An oversized body is reported as 413, not 400. BodyLimit's MaxBytesReader surfaces as a decode
// error, so without this branch the caller is told their JSON is malformed when it was perfectly
// well-formed and merely too large — and the one action that would fix it, sending less, is the one
// the message does not suggest.
func bindJSON(c *gin.Context, dst any) bool {
	dec := jsonDecoder(c.Request.Body)
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			failWith(c, http.StatusRequestEntityTooLarge, "body_too_large",
				"request body exceeds the maximum accepted size")
			return false
		}
		failWith(c, http.StatusBadRequest, "invalid_body", "request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

// uuidParam reads a uuid path parameter, answering 400 rather than looking it up and answering 404:
// a malformed id is a caller error, and reporting it as "not found" sends the reader hunting for a
// record that never existed.
func uuidParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		failWith(c, http.StatusBadRequest, "invalid_id", "not a valid id: "+c.Param(name))
		return uuid.Nil, false
	}
	return id, true
}

func queryInt(c *gin.Context, name string, def, min, max int) int {
	raw := c.Query(name)
	if raw == "" {
		return def
	}
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
		if n > max {
			return max
		}
	}
	if n < min {
		return min
	}
	return n
}
