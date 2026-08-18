package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwksMinRefresh bounds how often an unknown key id may trigger a fetch. Without it, a stream of
// tokens carrying a made-up kid turns into a stream of outbound requests to the provider.
const jwksMinRefresh = 60 * time.Second

// jwksMaxAge is how long a fetched key set is used before being refreshed on the next verification.
const jwksMaxAge = 6 * time.Hour

// jwksMaxBytes caps the response body. A provider that starts returning something enormous must not
// be able to exhaust this process's memory.
const jwksMaxBytes = 1 << 20

// IDTokenClaims is the subset of a verified ID token the control plane uses.
type IDTokenClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	// Nonce is carried through so the caller can bind a token to the authorization request that
	// asked for it. It is not validated here: this type does not know which request it belongs to.
	Nonce string
}

// OIDCVerifier verifies Google (or any OIDC provider's) ID tokens against the provider's published
// keys.
//
// The issuer and JWKS URL are injected rather than discovered, which is what lets the end-to-end
// suite point this exact code at an issuer it controls. The suite therefore exercises the real
// signature check, expiry check and audience check — the verifier is never stubbed out, so a
// regression in it cannot pass the tests.
type OIDCVerifier struct {
	issuer   string
	jwksURL  string
	clientID string
	client   *http.Client

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// NewOIDCVerifier builds a verifier. Every argument is required: an empty client id would make the
// audience check vacuous, which is exactly the shape of a control that passes having checked
// nothing.
func NewOIDCVerifier(issuer, jwksURL, clientID string, client *http.Client) (*OIDCVerifier, error) {
	switch {
	case issuer == "":
		return nil, errors.New("oidc issuer is required")
	case jwksURL == "":
		return nil, errors.New("oidc jwks url is required")
	case clientID == "":
		return nil, errors.New("oidc client id is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &OIDCVerifier{issuer: issuer, jwksURL: jwksURL, clientID: clientID, client: client}, nil
}

// Verify checks an ID token end to end and returns its claims.
//
// Failures are indistinguishable to the caller by design; the reason is wrapped for the server log
// only. Nothing in this function has a branch that returns claims on a verification error.
func (v *OIDCVerifier) Verify(ctx context.Context, rawToken string) (*IDTokenClaims, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("%w: empty token", ErrInvalidToken)
	}

	keyFunc := func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token has no kid")
		}
		key, err := v.keyByID(ctx, kid)
		if err != nil {
			return nil, err
		}
		return key, nil
	}

	parsed, err := jwt.Parse(rawToken, keyFunc,
		// RS256 only. Without this the "alg": "none" family and an HS256 token signed with the
		// provider's public modulus would both parse.
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.clientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		// Deliberately NOT WithStrictDecoding, which the session verifier does use. There it makes
		// our own credential a single string, because a malleable credential cannot be compared or
		// revoked by value. Here the token is Google's, this server keeps no state keyed on it, and
		// the four spellings of a signature are four spellings of a signature the caller still has
		// to have obtained from Google — so strictness buys nothing, and refusing a token an issuer
		// spelled unusually would lock every parent out of the console with no way in.
		jwt.WithLeeway(30*time.Second))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if !parsed.Valid {
		return nil, fmt.Errorf("%w: token not valid", ErrInvalidToken)
	}

	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected claim shape", ErrInvalidToken)
	}
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	verified, _ := claims["email_verified"].(bool)
	name, _ := claims["name"].(string)
	picture, _ := claims["picture"].(string)
	nonce, _ := claims["nonce"].(string)

	if sub == "" {
		return nil, fmt.Errorf("%w: token has no subject", ErrInvalidToken)
	}
	if email == "" {
		return nil, fmt.Errorf("%w: token has no email", ErrInvalidToken)
	}
	// An unverified address is rejected: parents are matched by email, so accepting one would let
	// anyone who can assert an arbitrary address at the provider become a parent.
	if !verified {
		return nil, fmt.Errorf("%w: email not verified", ErrInvalidToken)
	}

	return &IDTokenClaims{Subject: sub, Email: email, EmailVerified: true, Name: name,
		Picture: picture, Nonce: nonce}, nil
}

// keyByID returns the signing key for a kid, refreshing the key set when the id is unknown or the
// cached set is stale.
func (v *OIDCVerifier) keyByID(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	key, known := v.keys[kid]
	fresh := time.Since(v.fetchedAt) < jwksMaxAge
	canRefetch := time.Since(v.lastAttempt) >= jwksMinRefresh
	v.mu.Unlock()

	if known && fresh {
		return key, nil
	}
	if !canRefetch {
		if known {
			// The set is stale but the key is one we already trust and we refetched recently.
			// Using it beats failing every sign-in while the provider is briefly unreachable.
			return key, nil
		}
		return nil, fmt.Errorf("unknown key id %q and refresh rate-limited", kid)
	}

	if err := v.refresh(ctx); err != nil {
		if known {
			return key, nil
		}
		return nil, err
	}

	v.mu.Lock()
	key, known = v.keys[kid]
	v.mu.Unlock()
	if !known {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	return key, nil
}

type jwksDocument struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

// refresh fetches the provider's key set. It replaces the cache only on a fully successful parse
// that yielded at least one key: a half-parsed document must not be able to empty the cache and
// make every subsequent verification fail.
func (v *OIDCVerifier) refresh(ctx context.Context) error {
	v.mu.Lock()
	v.lastAttempt = time.Now()
	v.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBytes))
	if err != nil {
		return fmt.Errorf("read jwks: %w", err)
	}

	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("parse jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		if k.Alg != "" && k.Alg != jwt.SigningMethodRS256.Alg() {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("jwks contained no usable RSA keys")
	}

	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

func rsaPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	if len(nBytes) == 0 || len(eBytes) == 0 {
		return nil, errors.New("empty modulus or exponent")
	}
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
		return nil, errors.New("implausible exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(e.Int64())}, nil
}
