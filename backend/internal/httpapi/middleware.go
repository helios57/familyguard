// Package httpapi wires the control plane's HTTP surface: middleware, parent endpoints, device
// endpoints and the embedded console.
package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/store"
)

// Context keys. Values are read back with the helpers below rather than by string literal, so a
// typo cannot silently yield a zero value that reads as "not authenticated".
const (
	ctxRequestID = "fg.request_id"
	ctxParent    = "fg.parent"
	ctxDevice    = "fg.device"
	// ctxActorKind is set only when the parent was authenticated by an API key. Absent means a
	// person signed in, which is why the reader defaults to PARENT rather than requiring every
	// handler to know about keys.
	ctxActorKind = "fg.actor_kind"
)

// ctxAPIKeyActor is the audit actor type for a request that arrived with an API key.
const ctxAPIKeyActor = store.ActorAPIKey

// RequestID assigns every request an id, echoes it, and makes it available for log lines and error
// responses so a parent reporting a failure can be matched to a server-side record.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" || len(id) > 64 {
			id = uuid.NewString()
		}
		c.Set(ctxRequestID, id)
		c.Header("X-Request-Id", id)
		c.Next()
	}
}

// RequestIDOf returns the current request's id.
func RequestIDOf(c *gin.Context) string {
	v, _ := c.Get(ctxRequestID)
	s, _ := v.(string)
	return s
}

// contentSecurityPolicy is strict because the console is served by this binary and loads nothing
// from anywhere else. 'unsafe-inline' is absent for both script and style: the console keeps its
// CSS and JS in files, so there is nothing inline to allow.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self' data:; " +
	// No ws:/wss: — push is Server-Sent Events over the same origin, so a WebSocket scheme here
	// would widen the policy for a transport nothing in this binary speaks.
	"connect-src 'self'; " +
	"font-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// SecurityHeaders sets the response headers that hold for every route.
//
// HSTS is emitted only when the request actually arrived over TLS. Sending it over plaintext would
// be a claim the deployment cannot back, and it would pin a developer's http://localhost to https
// in their browser for a year.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=(), payment=()")
		if isTLS(c.Request) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// BodyLimit caps request bodies. Without it a single request can stream until the process runs out
// of memory, and no rate limit helps because it is one request.
func BodyLimit(maxBytes int64, larger map[string]int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}
		limit := maxBytes
		// Routed on the matched route, not on the raw URL. gin resolves the route before it runs
		// this chain, so c.FullPath() is the registered pattern — which means the exemption cannot
		// be reached by a path that merely looks like the upload endpoint, and cannot be missed by
		// one that carries a query string.
		//
		// The exemption is per route rather than global because raising the cap for everything is
		// the change that would actually cost something: every JSON endpoint would then accept
		// megabytes, and the body limit's job is to make an unauthenticated request cheap to refuse.
		if n, ok := larger[c.FullPath()]; ok && n > limit {
			limit = n
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

// CORS answers preflights and sets the response headers for exactly the configured origins.
//
// An empty list means same-origin only: no Access-Control-Allow-Origin header is emitted at all,
// which is the correct default when the console is served by this binary. There is no wildcard
// branch — config rejects "*" — so a misconfiguration cannot open the API to every origin.
func CORS(allowed []string) gin.HandlerFunc {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[strings.TrimSuffix(strings.ToLower(o), "/")] = true
	}
	return func(c *gin.Context) {
		origin := strings.TrimSuffix(strings.ToLower(c.GetHeader("Origin")), "/")
		if origin != "" && set[origin] {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", c.GetHeader("Origin"))
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
		}
		if c.Request.Method == http.MethodOptions {
			// A preflight from an origin that is not allowed gets 403, not 204: answering it
			// successfully would tell the caller the endpoint exists and is reachable.
			if origin == "" || !set[origin] {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// RateLimit applies the shared limiter, keyed by client address. gin's ClientIP honours
// X-Forwarded-For only for proxies the router trusts, which is configured explicitly at startup —
// otherwise any client could pick its own bucket by setting a header.
func RateLimit(limiter *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP()
		if key == "" {
			key = "unknown"
		}
		if !limiter.Allow(key) {
			c.Header("Retry-After", strconv.Itoa(1))
			abortWith(c, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		c.Next()
	}
}

// errorBody is the single error shape the API returns. Messages are deliberately generic on the
// authentication paths: a caller must not be able to tell a missing credential from a wrong one.
type errorBody struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func abortWith(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorBody{Error: code, Message: message, RequestID: RequestIDOf(c)})
}

func failWith(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorBody{Error: code, Message: message, RequestID: RequestIDOf(c)})
}
