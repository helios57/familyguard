// Package auth issues and verifies the two credentials in the system: a parent's session token,
// derived from a verified Google ID token, and a device's bearer token, issued once at enrollment.
//
// Every failure path in this package denies. There is no branch where a verification error, a
// network failure or an unparseable token results in an authenticated caller.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrInvalidToken is returned for every rejected credential. Callers map it to 401 without
// distinguishing the reason, so a probe cannot learn which part of a token was wrong.
var ErrInvalidToken = errors.New("invalid token")

// TokenBytes is the entropy of a device or enrollment token. 32 bytes from crypto/rand is well
// past any brute-force horizon and matches the SHA-256 digest stored beside it.
const TokenBytes = 32

// RecoveryCodeGroups and RecoveryCodeGroupLen shape the offline recovery code (FR-12.3): five
// groups of four Crockford-style base32 characters, which is legible over the phone and still
// carries ~100 bits.
const (
	RecoveryCodeGroups   = 5
	RecoveryCodeGroupLen = 4
	// RecoveryIterations is the PBKDF2 work factor the device applies before comparing. It is
	// stored per device rather than compiled in, so it can be raised without invalidating codes
	// already provisioned.
	RecoveryIterations = 120000
	recoveryKeyLen     = 32
)

// recoveryAlphabet omits I, L, O, U and the digits 0 and 1: a code read aloud must not depend on
// telling O from 0.
const recoveryAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewToken returns a fresh opaque token and its storage hash. Only the hash is ever persisted, so
// a database dump does not yield usable credentials.
func NewToken() (token string, hash []byte, err error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("read random: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken derives the stored form of a bearer token. SHA-256 is the right primitive here (rather
// than a password hash) because the input is 256 bits of uniform randomness: there is no dictionary
// to slow an attacker down against, and the lookup is on the hot path of every device request.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewRecoveryCode generates a per-device offline recovery code together with the PBKDF2 material
// the device embeds. The plaintext is returned once, for the console to show the parent; the device
// only ever receives salt, iterations and the derived hash, so extracting the DPC's storage does
// not reveal the code.
func NewRecoveryCode() (code string, salt []byte, iterations int, hash []byte, err error) {
	raw := make([]byte, RecoveryCodeGroups*RecoveryCodeGroupLen)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, 0, nil, fmt.Errorf("read random: %w", err)
	}
	var b strings.Builder
	for i, v := range raw {
		if i > 0 && i%RecoveryCodeGroupLen == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(recoveryAlphabet[int(v)%len(recoveryAlphabet)])
	}
	code = b.String()

	salt = make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", nil, 0, nil, fmt.Errorf("read random: %w", err)
	}
	iterations = RecoveryIterations
	hash, err = DeriveRecoveryHash(code, salt, iterations)
	if err != nil {
		return "", nil, 0, nil, err
	}
	return code, salt, iterations, hash, nil
}

// NormalizeRecoveryCode folds user input to the canonical form: uppercase, with every separator a
// keyboard, a chat app or an autocorrect may have left between the groups removed — wherever it
// appears, not only at the ends.
//
// The generous reading is deliberate. This code is typed on a phone that is by definition locked
// down and possibly offline, by a parent copying twenty characters off another screen; a rejection
// they cannot explain has no recovery path and no diagnostics. Nothing is given away by it: the
// alphabet holds no space and no dash, so a string that folds to the right code is the right code
// differently punctuated, never a different one.
//
// Two details are load-bearing, because the DPC mirrors this function in Kotlin
// (RecoveryCode.normalize) and the two must agree character for character:
//
//   - Spaces go through unicode.IsSpace, which is a *different set* from Java's
//     Character.isWhitespace: U+00A0 is in this one only, U+001C..U+001F in that one only. The
//     mirror spells Go's set out rather than calling the Java predicate.
//   - Dashes are enumerated rather than taken from unicode.Pd, because Go and an Android runtime
//     ship different Unicode table versions. A category membership that differs between them is a
//     code the console accepts and the phone rejects — the exact failure this exists to avoid — and
//     an enumerated set cannot drift.
//
// Uppercasing happens first, with strings.ToUpper's simple case mapping. Kotlin's String.uppercase()
// is the *full* mapping and turns ß into SS, so the mirror uses Character.toUpperCase(int).
// recovery-vectors.json pins all of it, and is replayed by both suites.
func NormalizeRecoveryCode(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range strings.ToUpper(raw) {
		if isRecoverySeparator(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isRecoverySeparator reports whether r carries no information about the code and is therefore
// dropped. See NormalizeRecoveryCode for why the dashes are a list rather than a Unicode category.
//
// Written as escapes, not as the characters themselves: half of these are indistinguishable from a
// hyphen in a monospace font and one of them (U+00AD) is invisible, so a literal list would be a
// line no reviewer could check and a substitution in it would not show up in a diff.
func isRecoverySeparator(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '-', // U+002D HYPHEN-MINUS — what NewRecoveryCode writes
		'\u00AD', // SOFT HYPHEN: invisible, so a parent could never see why the code failed
		'\u2010', // HYPHEN
		'\u2011', // NON-BREAKING HYPHEN
		'\u2012', // FIGURE DASH
		'\u2013', // EN DASH — what an autocorrect makes of a typed hyphen
		'\u2014', // EM DASH
		'\u2015', // HORIZONTAL BAR
		'\u2212', // MINUS SIGN
		'\uFE58', // SMALL EM DASH
		'\uFE63', // SMALL HYPHEN-MINUS
		'\uFF0D': // FULLWIDTH HYPHEN-MINUS
		return true
	}
	return false
}

// DeriveRecoveryHash derives the comparison value for a recovery code. The same function runs on
// the server and inside the DPC, over the normalized code, so the two cannot disagree about
// formatting.
func DeriveRecoveryHash(code string, salt []byte, iterations int) ([]byte, error) {
	if iterations < 1 {
		return nil, errors.New("recovery iterations must be positive")
	}
	return pbkdf2.Key(sha256.New, NormalizeRecoveryCode(code), salt, iterations, recoveryKeyLen)
}

// VerifyRecoveryCode compares in constant time. A derivation error denies rather than accepting:
// there is no path where failing to compute the hash lets a code through.
//
// The three refusals before the comparison mirror the DPC's RecoveryVerifier exactly, so the two
// sides deny the same inputs for the same reasons rather than both happening to deny:
//
//   - An entry that folds to nothing. An empty password is rejected outright by some PBKDF2
//     providers and hashed happily by others, so deriving from one would make "typed nothing" mean
//     different things on different phones. Here and there it means no.
//   - An empty salt, which is a device that never received material rather than one whose salt is
//     the empty string.
//   - An empty want, for the same reason: without it, a row with no stored hash would compare a
//     real digest against nothing.
func VerifyRecoveryCode(code string, salt []byte, iterations int, want []byte) bool {
	if NormalizeRecoveryCode(code) == "" || len(salt) == 0 || len(want) == 0 {
		return false
	}
	got, err := DeriveRecoveryHash(code, salt, iterations)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SessionClaims is what a parent's session token carries. Role is included so the API can make an
// authorization decision without a database round trip, and the parent id is the subject.
type SessionClaims struct {
	ParentID uuid.UUID
	Email    string
	Role     string
	Expires  time.Time
}

// SessionIssuer mints and verifies parent session tokens.
type SessionIssuer struct {
	key []byte
	ttl time.Duration
}

// NewSessionIssuer builds an issuer. The key length is validated in config; this constructor
// refuses a short key as well, so a caller that bypasses config cannot weaken the MAC.
func NewSessionIssuer(key []byte, ttl time.Duration) (*SessionIssuer, error) {
	if len(key) < 32 {
		return nil, errors.New("session signing key must be at least 32 bytes")
	}
	if ttl <= 0 {
		return nil, errors.New("session ttl must be positive")
	}
	return &SessionIssuer{key: key, ttl: ttl}, nil
}

// Issue mints a session token for a parent.
func (s *SessionIssuer) Issue(parentID uuid.UUID, email, role string, now time.Time) (string, time.Time, error) {
	exp := now.Add(s.ttl)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":   parentID.String(),
		"email": email,
		"role":  role,
		"iat":   now.Unix(),
		"exp":   exp.Unix(),
	})
	signed, err := tok.SignedString(s.key)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// Verify checks a session token. WithValidMethods pins HS256: without it a token whose header says
// "none" would be accepted, and a token signed with the RSA public key of the OIDC provider would
// too.
//
// WithStrictDecoding is what makes "a token is one string" true. An HMAC-SHA256 signature is 32
// bytes, which base64url spells in 43 characters — 258 bits of alphabet for 256 bits of signature,
// so the last character carries two bits that nothing reads. Without strict decoding those two bits
// are free: the same signature has four spellings, and changing the final character of a real token
// produces a different string that still authenticates. Measured before this option was added, on
// 400 freshly-minted tokens with the last character changed: 23 were accepted. That is not a forgery
// — the attacker still cannot produce a signature — but it means the credential is malleable, and a
// credential that is malleable cannot be compared, logged or revoked by its value. It also made the
// e2e row that changes one character of a signature pass 94% of the time, which is how it was found.
//
// Expiry is required twice — by WithExpirationRequired during parsing and by the exp == nil check
// below — and that is deliberate, not a leftover. Measured: removing either one alone leaves
// TestSessionTokensAreForgeryResistant green, because the other still rejects a token minted with
// no exp claim; only removing both lets one through. Nothing will go red if a future edit drops one
// of them, so neither is safe to delete on the grounds that the other covers it.
func (s *SessionIssuer) Verify(token string) (*SessionClaims, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithStrictDecoding())
	if err != nil || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrInvalidToken
	}
	sub, _ := claims["sub"].(string)
	id, err := uuid.Parse(sub)
	if err != nil {
		return nil, ErrInvalidToken
	}
	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return nil, ErrInvalidToken
	}
	email, _ := claims["email"].(string)
	role, _ := claims["role"].(string)
	if role == "" {
		return nil, ErrInvalidToken
	}
	return &SessionClaims{ParentID: id, Email: email, Role: role, Expires: exp.Time}, nil
}

// BearerToken extracts the credential from an Authorization header. A malformed header yields the
// empty string, which every caller treats as unauthenticated.
func BearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
