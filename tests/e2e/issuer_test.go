package e2e

// A local OpenID Connect provider, complete enough to drive both ways into the server:
// the ID-token endpoint and the browser authorization-code + PKCE flow.
//
// It exists so the real verifier runs. Stubbing the verifier out would leave the one piece of code
// standing between a stranger and a child's phone completely untested, and every negative control
// below would be asserting the behaviour of the stub.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	primaryKID = "e2e-primary"
	// impostorKID is served in the JWKS alongside the primary key, so a token signed by the impostor
	// key but labelled with the primary kid is a *signature* failure and not an unknown-key failure.
	// Those are different bugs and a test that cannot tell them apart proves the weaker one.
	impostorKID = "e2e-impostor"
)

type issuer struct {
	srv *httptest.Server

	primary  *rsa.PrivateKey
	impostor *rsa.PrivateKey

	mu sync.Mutex
	// pending maps an authorization code to what /token must check and mint.
	pending map[string]pendingAuth
	// nextLogin is the identity the next /authorize will sign in. The browser flow has no other way
	// to say who is at the keyboard.
	nextLogin identity
	// tokenStatus, when non-zero, makes /token answer with it instead of a token. Used to prove the
	// callback fails closed when the provider misbehaves.
	tokenStatus int
}

type identity struct {
	Email    string
	Subject  string
	Name     string
	Verified bool
}

type pendingAuth struct {
	challenge string
	nonce     string
	who       identity
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	primary, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate primary key: %v", err)
	}
	impostor, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate impostor key: %v", err)
	}

	is := &issuer{primary: primary, impostor: impostor, pending: map[string]pendingAuth{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", is.serveJWKS)
	mux.HandleFunc("/authorize", is.serveAuthorize)
	mux.HandleFunc("/token", is.serveToken)
	is.srv = httptest.NewServer(mux)
	t.Cleanup(is.srv.Close)
	return is
}

func (is *issuer) URL() string     { return is.srv.URL }
func (is *issuer) JWKSURL() string { return is.srv.URL + "/jwks" }
func (is *issuer) AuthURL() string { return is.srv.URL + "/authorize" }
func (is *issuer) TokenURL() string {
	return is.srv.URL + "/token"
}

func (is *issuer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	type jwk struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	pub := func(kid string, k *rsa.PrivateKey) jwk {
		return jwk{
			Kty: "RSA", Kid: kid, Use: "sig", Alg: "RS256",
			N: b64(k.PublicKey.N.Bytes()),
			E: b64(big.NewInt(int64(k.PublicKey.E)).Bytes()),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	// Only the primary key is published. The impostor's public half is deliberately absent, which is
	// what makes a token signed with it a forgery rather than a second valid signer.
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []jwk{pub(primaryKID, is.primary)}})
}

// serveAuthorize plays the consent screen: it records the PKCE challenge and bounces the browser
// back to the server's callback with a code.
func (is *issuer) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	if redirect == "" {
		http.Error(w, "no redirect_uri", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "this provider requires PKCE with S256", http.StatusBadRequest)
		return
	}

	code := randomString()
	is.mu.Lock()
	is.pending[code] = pendingAuth{
		challenge: q.Get("code_challenge"),
		nonce:     q.Get("nonce"),
		who:       is.nextLogin,
	}
	is.mu.Unlock()

	back, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	rq := back.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	back.RawQuery = rq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// serveToken performs the code exchange, verifying the PKCE verifier the way a real provider does.
func (is *issuer) serveToken(w http.ResponseWriter, r *http.Request) {
	is.mu.Lock()
	status := is.tokenStatus
	is.mu.Unlock()
	if status != 0 {
		http.Error(w, "provider is unhappy", status)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("client_secret") == "" {
		http.Error(w, "no client_secret", http.StatusUnauthorized)
		return
	}
	code := r.PostForm.Get("code")

	is.mu.Lock()
	auth, ok := is.pending[code]
	// A code is single-use here too, so a replayed callback cannot mint a second session.
	delete(is.pending, code)
	is.mu.Unlock()
	if !ok {
		http.Error(w, "unknown code", http.StatusBadRequest)
		return
	}

	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if b64(sum[:]) != auth.challenge {
		http.Error(w, "pkce verifier does not match the challenge", http.StatusBadRequest)
		return
	}

	token := is.mint(tokenSpec{
		Who:      auth.who,
		Audience: r.PostForm.Get("client_id"),
		Nonce:    auth.nonce,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "not-used-by-this-server",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     token,
	})
}

// setNextLogin decides who the next browser sign-in will be.
func (is *issuer) setNextLogin(who identity) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.nextLogin = who
}

func (is *issuer) setTokenStatus(status int) {
	is.mu.Lock()
	defer is.mu.Unlock()
	is.tokenStatus = status
}

// ---- token minting --------------------------------------------------------

// tokenSpec is written so that every negative control is one field away from the valid token. A
// suite that builds its bad tokens by a different route than its good ones can pass while agreeing
// with a bug in the route it does not share.
type tokenSpec struct {
	Who      identity
	Audience string
	Nonce    string

	// Overrides for the negative controls. Zero values mean "use the correct thing".
	Issuer      string
	NotBefore   time.Time
	IssuedAt    time.Time
	Expires     time.Time
	KID         string
	Alg         string
	SignWith    *rsa.PrivateKey
	OmitExpiry  bool
	OmitSubject bool
	OmitEmail   bool
}

func (is *issuer) mint(spec tokenSpec) string {
	now := time.Now()
	iss := spec.Issuer
	if iss == "" {
		iss = is.srv.URL
	}
	iat := spec.IssuedAt
	if iat.IsZero() {
		iat = now
	}
	exp := spec.Expires
	if exp.IsZero() {
		exp = now.Add(time.Hour)
	}
	kid := spec.KID
	if kid == "" {
		kid = primaryKID
	}
	alg := spec.Alg
	if alg == "" {
		alg = "RS256"
	}
	key := spec.SignWith
	if key == nil {
		key = is.primary
	}

	claims := map[string]any{
		"iss":            iss,
		"aud":            spec.Audience,
		"iat":            iat.Unix(),
		"email":          spec.Who.Email,
		"email_verified": spec.Who.Verified,
		"name":           spec.Who.Name,
		"sub":            spec.Who.Subject,
	}
	if !spec.OmitExpiry {
		claims["exp"] = exp.Unix()
	}
	if spec.OmitSubject {
		delete(claims, "sub")
	}
	if spec.OmitEmail {
		delete(claims, "email")
	}
	if spec.Nonce != "" {
		claims["nonce"] = spec.Nonce
	}
	if !spec.NotBefore.IsZero() {
		claims["nbf"] = spec.NotBefore.Unix()
	}

	headerJSON, err := json.Marshal(map[string]any{"alg": alg, "typ": "JWT", "kid": kid})
	if err != nil {
		panic(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	signingInput := b64(headerJSON) + "." + b64(claimsJSON)

	var sig []byte
	switch alg {
	case "none":
		sig = nil
	default:
		digest := sha256.Sum256([]byte(signingInput))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			panic(err)
		}
	}
	return signingInput + "." + b64(sig)
}

// validToken is the happy path, so every test that only needs a working sign-in says so in one line.
func (is *issuer) validToken(clientID string, who identity) string {
	return is.mint(tokenSpec{Who: who, Audience: clientID})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randomString() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return strings.ToLower(base64.RawURLEncoding.EncodeToString(buf))
}
