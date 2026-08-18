package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// issuerFixture is a real OIDC issuer: a live HTTP server publishing a JWKS document for a key it
// actually signs with. The verifier under test performs the same fetch, the same signature check
// and the same claim checks it performs against Google — nothing is stubbed, so a regression in
// any of those checks fails these tests.
type issuerFixture struct {
	t        *testing.T
	key      *rsa.PrivateKey
	kid      string
	server   *httptest.Server
	issuer   string
	clientID string
	fetches  atomic.Int64
}

func newIssuerFixture(t *testing.T) *issuerFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	f := &issuerFixture{t: t, key: key, kid: "test-key-1", clientID: "family-guard-test-client"}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		f.fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": f.kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	f.server = httptest.NewServer(mux)
	f.issuer = f.server.URL
	t.Cleanup(f.server.Close)
	return f
}

func (f *issuerFixture) verifier() *OIDCVerifier {
	f.t.Helper()
	v, err := NewOIDCVerifier(f.issuer, f.server.URL+"/jwks", f.clientID, f.server.Client())
	if err != nil {
		f.t.Fatalf("new verifier: %v", err)
	}
	return v
}

func (f *issuerFixture) claims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":            f.issuer,
		"aud":            f.clientID,
		"sub":            "1234567890",
		"email":          "parent@example.com",
		"email_verified": true,
		"name":           "Test Parent",
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}
}

func (f *issuerFixture) sign(claims jwt.MapClaims) string {
	f.t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(f.key)
	if err != nil {
		f.t.Fatalf("sign: %v", err)
	}
	return s
}

// TestVerifyAcceptsGenuineToken is the positive half of the calibration. Without it, every
// rejection below could be explained by a verifier that rejects everything.
func TestVerifyAcceptsGenuineToken(t *testing.T) {
	f := newIssuerFixture(t)
	got, err := f.verifier().Verify(context.Background(), f.sign(f.claims()))
	if err != nil {
		t.Fatalf("genuine token rejected: %v", err)
	}
	if got.Email != "parent@example.com" || got.Subject != "1234567890" || !got.EmailVerified {
		t.Fatalf("claims not returned faithfully: %+v", got)
	}
}

// TestVerifyRejects drives one deliberate defect per case. Each case is a known-bad input: if the
// corresponding check were deleted, that case would turn green, which is what makes this suite
// able to fail.
func TestVerifyRejects(t *testing.T) {
	cases := []struct {
		name  string
		token func(f *issuerFixture) string
	}{
		{"wrong audience", func(f *issuerFixture) string {
			c := f.claims()
			c["aud"] = "some-other-client"
			return f.sign(c)
		}},
		{"wrong issuer", func(f *issuerFixture) string {
			c := f.claims()
			c["iss"] = "https://evil.example"
			return f.sign(c)
		}},
		{"expired", func(f *issuerFixture) string {
			c := f.claims()
			c["exp"] = time.Now().Add(-2 * time.Hour).Unix()
			c["iat"] = time.Now().Add(-3 * time.Hour).Unix()
			return f.sign(c)
		}},
		{"no expiry at all", func(f *issuerFixture) string {
			c := f.claims()
			delete(c, "exp")
			return f.sign(c)
		}},
		{"email not verified", func(f *issuerFixture) string {
			c := f.claims()
			c["email_verified"] = false
			return f.sign(c)
		}},
		{"email missing", func(f *issuerFixture) string {
			c := f.claims()
			delete(c, "email")
			return f.sign(c)
		}},
		{"alg none", func(f *issuerFixture) string {
			tok := jwt.NewWithClaims(jwt.SigningMethodNone, f.claims())
			tok.Header["kid"] = f.kid
			s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
			if err != nil {
				t.Fatalf("sign none: %v", err)
			}
			return s
		}},
		{"HS256 signed with the public modulus", func(f *issuerFixture) string {
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, f.claims())
			tok.Header["kid"] = f.kid
			s, err := tok.SignedString(f.key.N.Bytes())
			if err != nil {
				t.Fatalf("sign hs256: %v", err)
			}
			return s
		}},
		{"signed by a different key", func(f *issuerFixture) string {
			other, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims())
			tok.Header["kid"] = f.kid
			s, err := tok.SignedString(other)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			return s
		}},
		{"unknown key id", func(f *issuerFixture) string {
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims())
			tok.Header["kid"] = "not-a-key-we-publish"
			s, err := tok.SignedString(f.key)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			return s
		}},
		{"no key id", func(f *issuerFixture) string {
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims())
			s, err := tok.SignedString(f.key)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			return s
		}},
		{"empty token", func(f *issuerFixture) string { return "" }},
		{"garbage", func(f *issuerFixture) string { return "not.a.token" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssuerFixture(t)
			claims, err := f.verifier().Verify(context.Background(), tc.token(f))
			if err == nil {
				t.Fatalf("accepted a token that must be rejected: %+v", claims)
			}
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("error must wrap ErrInvalidToken so handlers map it to 401, got %v", err)
			}
			if claims != nil {
				t.Fatalf("claims returned alongside an error: %+v", claims)
			}
		})
	}
}

// TestVerifyDoesNotFetchJWKSPerToken pins the cache. A verifier that refetched on every call would
// still pass every test above while making the provider rate-limit sign-in under load.
func TestVerifyDoesNotFetchJWKSPerToken(t *testing.T) {
	f := newIssuerFixture(t)
	v := f.verifier()
	for i := 0; i < 5; i++ {
		if _, err := v.Verify(context.Background(), f.sign(f.claims())); err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
	}
	if got := f.fetches.Load(); got != 1 {
		t.Fatalf("expected exactly one JWKS fetch for five tokens, got %d", got)
	}
}

// TestUnknownKidRefreshIsRateLimited proves the refresh cannot be used as an amplifier: a burst of
// tokens carrying a made-up kid must not become a burst of outbound requests.
func TestUnknownKidRefreshIsRateLimited(t *testing.T) {
	f := newIssuerFixture(t)
	v := f.verifier()
	bad := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, f.claims())
		tok.Header["kid"] = "made-up"
		s, err := tok.SignedString(f.key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
	}
	for i := 0; i < 10; i++ {
		if _, err := v.Verify(context.Background(), bad()); err == nil {
			t.Fatal("token with an unknown kid was accepted")
		}
	}
	if got := f.fetches.Load(); got > 1 {
		t.Fatalf("expected at most one JWKS fetch for ten unknown-kid tokens, got %d", got)
	}
}

// TestVerifierRequiresConfiguration keeps the audience check from becoming vacuous: an empty client
// id would make every token's audience acceptable.
func TestVerifierRequiresConfiguration(t *testing.T) {
	for _, tc := range []struct{ issuer, jwks, client string }{
		{"", "https://x/jwks", "cid"},
		{"https://x", "", "cid"},
		{"https://x", "https://x/jwks", ""},
	} {
		if _, err := NewOIDCVerifier(tc.issuer, tc.jwks, tc.client, nil); err == nil {
			t.Fatalf("accepted incomplete configuration %+v", tc)
		}
	}
}

// TestRefreshKeepsCacheOnBadDocument: a provider blip must not empty a working cache, because that
// would take every sign-in down until the provider recovered.
func TestRefreshKeepsCacheOnBadDocument(t *testing.T) {
	f := newIssuerFixture(t)
	v := f.verifier()
	if _, err := v.Verify(context.Background(), f.sign(f.claims())); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer broken.Close()
	v.jwksURL = broken.URL

	v.mu.Lock()
	v.fetchedAt = time.Now().Add(-2 * jwksMaxAge)
	v.lastAttempt = time.Now().Add(-2 * jwksMinRefresh)
	v.mu.Unlock()

	if _, err := v.Verify(context.Background(), f.sign(f.claims())); err != nil {
		t.Fatalf("a failed refresh must not invalidate a key already trusted: %v", err)
	}
}
