package auth

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestNewTokenIsRandomAndHashMatches(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, hash, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if seen[tok] {
			t.Fatal("NewToken repeated a value")
		}
		seen[tok] = true
		if len(tok) < 40 {
			t.Fatalf("token too short: %d chars", len(tok))
		}
		if !bytes.Equal(hash, HashToken(tok)) {
			t.Fatal("returned hash does not match HashToken of the token")
		}
	}
}

// TestHashTokenDiscriminates is the calibration for the device-token lookup: if HashToken returned
// a constant, every store lookup would still "work" and every device would authenticate as the
// first one.
func TestHashTokenDiscriminates(t *testing.T) {
	if bytes.Equal(HashToken("a"), HashToken("b")) {
		t.Fatal("HashToken collided on distinct inputs")
	}
	if !bytes.Equal(HashToken("a"), HashToken("a")) {
		t.Fatal("HashToken is not deterministic")
	}
	if len(HashToken("a")) != 32 {
		t.Fatalf("expected a 32-byte digest, got %d", len(HashToken("a")))
	}
}

func TestRecoveryCodeRoundTrip(t *testing.T) {
	code, salt, iter, hash, err := NewRecoveryCode()
	if err != nil {
		t.Fatalf("NewRecoveryCode: %v", err)
	}
	if strings.Count(code, "-") != RecoveryCodeGroups-1 {
		t.Fatalf("unexpected code shape: %q", code)
	}
	for _, forbidden := range []string{"0", "1", "I", "L", "O", "U"} {
		if strings.Contains(code, forbidden) {
			t.Fatalf("code contains an ambiguous character %q: %q", forbidden, code)
		}
	}
	if !VerifyRecoveryCode(code, salt, iter, hash) {
		t.Fatal("the generated code does not verify against its own hash")
	}
	// Formatting must not matter: a parent reads this over the phone.
	if !VerifyRecoveryCode(strings.ToLower(strings.ReplaceAll(code, "-", " ")), salt, iter, hash) {
		t.Fatal("normalisation failed: a differently formatted code was rejected")
	}
	// Negative control. Without this, a VerifyRecoveryCode that returned true unconditionally
	// would pass every assertion above.
	if VerifyRecoveryCode("AAAA-AAAA-AAAA-AAAA-AAAA", salt, iter, hash) {
		t.Fatal("a wrong code verified")
	}
	if VerifyRecoveryCode(code, salt, iter, nil) {
		t.Fatal("verification against an absent hash succeeded")
	}
	if VerifyRecoveryCode(code, salt, 0, hash) {
		t.Fatal("verification with a zero work factor succeeded")
	}
}

func TestRecoveryCodesAreUniquePerDevice(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, _, _, _, err := NewRecoveryCode()
		if err != nil {
			t.Fatalf("NewRecoveryCode: %v", err)
		}
		if seen[code] {
			t.Fatal("recovery code repeated — a shared code across devices is exactly the draft's defect")
		}
		seen[code] = true
	}
}

func TestSessionRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	iss, err := NewSessionIssuer(key, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionIssuer: %v", err)
	}
	id := uuid.New()
	tok, exp, err := iss.Issue(id, "p@example.com", "ADMIN", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !exp.After(time.Now()) {
		t.Fatal("issued token already expired")
	}
	claims, err := iss.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.ParentID != id || claims.Email != "p@example.com" || claims.Role != "ADMIN" {
		t.Fatalf("claims not round-tripped: %+v", claims)
	}
}

func TestSessionRejects(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	iss, _ := NewSessionIssuer(key, time.Hour)
	other, _ := NewSessionIssuer(bytes.Repeat([]byte("x"), 32), time.Hour)
	id := uuid.New()
	good, _, _ := iss.Issue(id, "p@example.com", "ADMIN", time.Now())

	t.Run("signed with a different key", func(t *testing.T) {
		tok, _, _ := other.Issue(id, "p@example.com", "ADMIN", time.Now())
		if _, err := iss.Verify(tok); err == nil {
			t.Fatal("accepted a token signed with another key")
		}
	})
	t.Run("expired", func(t *testing.T) {
		tok, _, _ := iss.Issue(id, "p@example.com", "ADMIN", time.Now().Add(-2*time.Hour))
		if _, err := iss.Verify(tok); err == nil {
			t.Fatal("accepted an expired token")
		}
	})
	t.Run("alg none", func(t *testing.T) {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
			"sub": id.String(), "role": "PRIMARY_ADMIN", "exp": time.Now().Add(time.Hour).Unix(),
		}).SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := iss.Verify(tok); err == nil {
			t.Fatal("accepted an unsigned token")
		}
	})
	t.Run("tampered payload", func(t *testing.T) {
		parts := strings.Split(good, ".")
		payload := []byte(parts[1])
		mid := len(payload) / 2
		if payload[mid] == 'A' {
			payload[mid] = 'B'
		} else {
			payload[mid] = 'A'
		}
		parts[1] = string(payload)
		if strings.Join(parts, ".") == good {
			t.Fatal("tamper did not change the token — this case would prove nothing")
		}
		if _, err := iss.Verify(strings.Join(parts, ".")); err == nil {
			t.Fatal("accepted a token whose payload was altered")
		}
	})
	t.Run("no expiry", func(t *testing.T) {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": id.String(), "role": "ADMIN",
		}).SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := iss.Verify(tok); err == nil {
			t.Fatal("accepted a token that never expires")
		}
	})
	t.Run("no role", func(t *testing.T) {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": id.String(), "exp": time.Now().Add(time.Hour).Unix(),
		}).SignedString(key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := iss.Verify(tok); err == nil {
			t.Fatal("accepted a token carrying no role")
		}
	})
	t.Run("empty", func(t *testing.T) {
		if _, err := iss.Verify(""); err == nil {
			t.Fatal("accepted an empty token")
		}
	})
	// A signature respelled rather than changed.
	//
	// base64url spells a 32-byte HMAC in 43 characters — 258 bits of alphabet for 256 bits of
	// signature — so the last character carries two bits that decode to nothing, and four different
	// characters mean the same signature. A verifier that ignores those bits accepts four strings
	// for one credential, and "one character was changed" stops being a true statement about it.
	// This is not hypothetical: it is how the e2e row that changes one character of a signature came
	// to pass 94% of the time, silently, until the 6% turned up.
	//
	// Sixteen tokens rather than one, because the property is about the *last* character and one
	// token only exercises whichever character it happened to end with.
	t.Run("a signature respelled with non-zero padding bits", func(t *testing.T) {
		const b64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
		for i := 0; i < 16; i++ {
			tok, _, err := iss.Issue(uuid.New(), "p@example.com", "ADMIN", time.Now())
			if err != nil {
				t.Fatalf("issue: %v", err)
			}
			cut := strings.LastIndex(tok, ".")
			seg := tok[cut+1:]
			idx := strings.IndexByte(b64url, seg[len(seg)-1])
			if idx < 0 || idx&0x03 != 0 {
				t.Fatalf("the issuer minted a non-canonical signature ending %q; this case assumes it does not",
					seg[len(seg)-1])
			}
			respelled := seg[:len(seg)-1] + string(b64url[idx|0x01])
			// The whole point of the case: same bytes, different string. Asserted rather than
			// assumed, because a respelling that changed the signature would be a second copy of
			// the "tampered payload" case above and would pass for the wrong reason.
			was, err1 := base64.RawURLEncoding.DecodeString(seg)
			now, err2 := base64.RawURLEncoding.DecodeString(respelled)
			if err1 != nil || err2 != nil || !bytes.Equal(was, now) {
				t.Fatalf("the respelling changed the signature bytes (%v/%v); it would prove nothing", err1, err2)
			}
			if _, err := iss.Verify(tok[:cut+1] + respelled); err == nil {
				t.Fatalf("accepted a second spelling of a valid signature (ends %q -> %q)",
					seg[len(seg)-1], b64url[idx|0x01])
			}
		}
	})

	// Positive control: the same verifier still accepts the genuine token, so the rejections above
	// are not a verifier that rejects everything.
	if _, err := iss.Verify(good); err != nil {
		t.Fatalf("genuine token rejected: %v", err)
	}
}

func TestSessionIssuerRefusesWeakKey(t *testing.T) {
	if _, err := NewSessionIssuer([]byte("short"), time.Hour); err == nil {
		t.Fatal("accepted a signing key shorter than the digest size")
	}
	if _, err := NewSessionIssuer(bytes.Repeat([]byte("k"), 32), 0); err == nil {
		t.Fatal("accepted a zero session lifetime")
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":  "abc",
		"bearer abc":  "abc",
		"BEARER abc":  "abc",
		"Bearer  abc": "abc",
		"abc":         "",
		"Bearer":      "",
		"Bearer ":     "",
		"":            "",
		"Basic abc":   "",
	}
	for header, want := range cases {
		if got := BearerToken(header); got != want {
			t.Fatalf("BearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}
