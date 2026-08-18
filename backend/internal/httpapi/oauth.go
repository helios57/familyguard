package httpapi

// Browser sign-in, as an OAuth 2.0 authorization-code exchange with PKCE that this server performs
// itself.
//
// The alternative — dropping Google's Identity Services script into the console and letting it hand
// the page an ID token — is one line of HTML and it is the reason this file exists instead. That
// script runs with the console's own origin: it can read the session token, call the API as the
// parent, and change what the page shows. Redirecting to the provider and back keeps every third
// party outside the origin, which is also what lets the console keep `script-src 'self'`.
//
// The session token comes back in the URL *fragment*. A query parameter would be written to every
// access log, proxy log and Referer header between here and the browser; a fragment is never sent
// to a server at all.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/store"
)

const (
	// oauthHandshakeCookie holds the CSRF state and the PKCE verifier for the few seconds between
	// leaving for the provider and coming back. It is not a session: it is deleted at the callback,
	// whether that callback succeeded or failed.
	oauthHandshakeCookie = "fg_oauth"
	oauthHandshakeTTL    = 10 * time.Minute
	oauthCallbackPath    = "/api/v1/auth/google/callback"
)

// loginConfigured reports whether the browser flow can run at all. Without a client secret the
// server can still accept an ID token on POST /auth/google, but it cannot complete a code exchange.
func (s *Server) loginConfigured() bool {
	return s.cfg.OAuthClientSecret != "" && s.cfg.OAuthAuthURL != "" && s.cfg.OAuthTokenURL != ""
}

// oauthStart sends the browser to the identity provider.
func (s *Server) oauthStart(c *gin.Context) {
	if !s.loginConfigured() {
		failWith(c, http.StatusNotImplemented, "misconfigured",
			"browser sign-in is not configured on this server")
		return
	}
	state, err := randomURLSafe(24)
	if err != nil {
		s.fail(c, err)
		return
	}
	verifier, err := randomURLSafe(32)
	if err != nil {
		s.fail(c, err)
		return
	}
	// The nonce is bound into the handshake cookie and checked against the ID token at the callback.
	// It is what stops a token minted for a different authorization request from being replayed
	// into this one.
	nonce, err := randomURLSafe(24)
	if err != nil {
		s.fail(c, err)
		return
	}

	s.setHandshakeCookie(c, strings.Join([]string{state, verifier, nonce}, "."))

	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"client_id":             {s.cfg.OAuthClientID},
		"redirect_uri":          {s.redirectURI()},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		// Without this, a parent who is signed into several Google accounts silently gets whichever
		// one the browser picked last, and the only symptom is "sign-in failed".
		"prompt": {"select_account"},
	}
	c.Redirect(http.StatusFound, s.cfg.OAuthAuthURL+"?"+q.Encode())
}

// oauthCallback completes the exchange and hands the console a session.
//
// Every failure lands the browser back on the console with `#error=…` rather than on a JSON error
// page: a parent who mistyped their password on a phone should see the console again, not a stack
// of curly braces. The reason is logged server-side with the request id.
func (s *Server) oauthCallback(c *gin.Context) {
	fail := func(reason string, err error) {
		s.log.Warn("browser sign-in refused", "reason", reason, "error", err,
			"request_id", RequestIDOf(c), "client", c.ClientIP())
		c.Redirect(http.StatusFound, "/#error=login_failed")
	}

	raw, err := c.Cookie(oauthHandshakeCookie)
	s.clearHandshakeCookie(c)
	if err != nil {
		fail("no handshake cookie", err)
		return
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		fail("malformed handshake cookie", nil)
		return
	}
	state, verifier, nonce := parts[0], parts[1], parts[2]

	if c.Query("error") != "" {
		fail("provider returned an error: "+c.Query("error"), nil)
		return
	}
	// Compared against the cookie, so a callback the parent's browser did not start cannot sign
	// anyone in — including into an attacker's account.
	if got := c.Query("state"); got == "" || got != state {
		fail("state mismatch", nil)
		return
	}
	code := c.Query("code")
	if code == "" {
		fail("no authorization code", nil)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	idToken, err := s.exchangeCode(ctx, code, verifier)
	if err != nil {
		fail("code exchange failed", err)
		return
	}
	claims, err := s.verifier.Verify(ctx, idToken)
	if err != nil {
		fail("token rejected", err)
		return
	}
	if claims.Nonce != nonce {
		fail("nonce mismatch", nil)
		return
	}

	session, err := s.issueSession(c, claims)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Distinguished for the parent, not for an attacker: reaching this point required
			// completing a real sign-in with the provider, so "you are not a parent of this family"
			// tells the person in front of the phone something they need and an outsider nothing
			// they could not already learn by trying.
			c.Redirect(http.StatusFound, "/#error=not_a_parent")
			return
		}
		fail("session issue failed", err)
		return
	}

	frag := url.Values{
		"token":   {session.Token},
		"expires": {session.ExpiresAt.Format(time.RFC3339)},
	}
	c.Redirect(http.StatusFound, "/#"+frag.Encode())
}

// exchangeCode trades the authorization code for an ID token.
func (s *Server) exchangeCode(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {s.redirectURI()},
		"client_id":     {s.cfg.OAuthClientID},
		"client_secret": {s.cfg.OAuthClientSecret},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.OAuthTokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The body can carry the client secret back in an error echo, so it is not logged.
		return "", errors.New("token endpoint returned " + resp.Status)
	}

	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.IDToken == "" {
		return "", errors.New("token endpoint returned no id_token")
	}
	return body.IDToken, nil
}

func (s *Server) redirectURI() string {
	return strings.TrimSuffix(s.cfg.PublicURL.String(), "/") + oauthCallbackPath
}

// setHandshakeCookie writes the short-lived handshake cookie.
//
// HttpOnly keeps the PKCE verifier out of reach of any script. SameSite=Lax is the value that makes
// this work at all: the callback is a top-level navigation from the provider, which Strict would
// drop — turning every sign-in into "no handshake cookie".
func (s *Server) setHandshakeCookie(c *gin.Context, value string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthHandshakeCookie,
		Value:    value,
		Path:     "/api/v1/auth",
		MaxAge:   int(oauthHandshakeTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.PublicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearHandshakeCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oauthHandshakeCookie,
		Value:    "",
		Path:     "/api/v1/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.PublicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
