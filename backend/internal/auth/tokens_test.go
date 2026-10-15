package auth

import (
	"bytes"
	"strings"
	"testing"
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
