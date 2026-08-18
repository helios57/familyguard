package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
	"unicode"
)

type normalizeVector struct {
	Name   string `json:"name"`
	Raw    string `json:"raw"`
	Expect string `json:"expect"`
}

type deriveVector struct {
	Name       string `json:"name"`
	Code       string `json:"code"`
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	ExpectHash string `json:"expect_hash"`
}

type verifyVector struct {
	Name       string `json:"name"`
	Code       string `json:"code"`
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	Hash       string `json:"hash"`
	Expect     bool   `json:"expect"`
}

// decodeVectors pulls one section out of the shared file.
//
// DisallowUnknownFields throughout: a misspelled key would otherwise be dropped in silence and the
// vector would assert against a zero value instead of the one that was written down — an
// `iterations` typo would make every derive case run at zero rounds and refuse, which reads as a
// correctly failing implementation.
func decodeVectors[T any](t *testing.T, section string) []T {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(RecoveryVectors, &top); err != nil {
		t.Fatalf("shared recovery vectors are not valid JSON: %v", err)
	}
	raw, ok := top[section]
	if !ok {
		t.Fatalf("shared recovery vectors have no %q section", section)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("%q is not an array: %v", section, err)
	}
	if len(items) == 0 {
		t.Fatalf("the %q section is empty, so replaying it asserts nothing", section)
	}
	out := make([]T, 0, len(items))
	for i, item := range items {
		dec := json.NewDecoder(bytes.NewReader(item))
		dec.DisallowUnknownFields()
		var v T
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s vector %d is malformed: %v", section, i, err)
		}
		out = append(out, v)
	}
	return out
}

func mustDecodeB64(t *testing.T, value string) []byte {
	t.Helper()
	if value == "" {
		return nil
	}
	// RawURLEncoding, matching what the API emits and what the DPC decodes. Padded input is a
	// vector that was written by hand against the wrong encoder, so it fails here rather than
	// silently producing different bytes on the two sides.
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("vector value %q is not base64 RawURL: %v", value, err)
	}
	return decoded
}

// TestRecoveryVectors replays the shared file. It is the anti-drift control for FR-12.3: the same
// vectors run in the DPC's JVM suite, so a normaliser or a work factor the two sides disagree about
// turns a build red instead of producing a phone that rejects the code the parent was shown.
//
// Note what the file is NOT: it is not generated from this package. The normalisation expectations
// are hand-written from the rule in NormalizeRecoveryCode's doc comment and the digests come from
// Python's hashlib — a third PBKDF2 implementation. Regenerating it from Go output would leave it
// asserting only that Go does what Go does.
func TestRecoveryVectors(t *testing.T) {
	t.Run("normalize", func(t *testing.T) {
		seen := map[string]bool{}
		for _, v := range decodeVectors[normalizeVector](t, "normalize") {
			if v.Name == "" {
				t.Fatal("a normalize vector has no name")
			}
			if seen[v.Name] {
				t.Fatalf("duplicate normalize vector %q — a rename would silently drop a case", v.Name)
			}
			seen[v.Name] = true
			if got := NormalizeRecoveryCode(v.Raw); got != v.Expect {
				t.Errorf("%s: NormalizeRecoveryCode(%q) = %q, want %q", v.Name, v.Raw, got, v.Expect)
			}
		}
	})

	t.Run("derive", func(t *testing.T) {
		seen := map[string]bool{}
		for _, v := range decodeVectors[deriveVector](t, "derive") {
			if v.Name == "" {
				t.Fatal("a derive vector has no name")
			}
			if seen[v.Name] {
				t.Fatalf("duplicate derive vector %q", v.Name)
			}
			seen[v.Name] = true

			want := mustDecodeB64(t, v.ExpectHash)
			if len(want) != recoveryKeyLen {
				t.Fatalf("%s: expects a %d-byte digest, not %d — the vector and the key length disagree",
					v.Name, recoveryKeyLen, len(want))
			}
			got, err := DeriveRecoveryHash(v.Code, mustDecodeB64(t, v.Salt), v.Iterations)
			if err != nil {
				t.Fatalf("%s: DeriveRecoveryHash: %v", v.Name, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: digest is %s, want %s",
					v.Name,
					base64.RawURLEncoding.EncodeToString(got),
					base64.RawURLEncoding.EncodeToString(want))
			}
		}
	})

	t.Run("verify", func(t *testing.T) {
		seen := map[string]bool{}
		for _, v := range decodeVectors[verifyVector](t, "verify") {
			if v.Name == "" {
				t.Fatal("a verify vector has no name")
			}
			if seen[v.Name] {
				t.Fatalf("duplicate verify vector %q", v.Name)
			}
			seen[v.Name] = true

			got := VerifyRecoveryCode(v.Code, mustDecodeB64(t, v.Salt), v.Iterations, mustDecodeB64(t, v.Hash))
			if got != v.Expect {
				t.Errorf("%s: VerifyRecoveryCode = %v, want %v", v.Name, got, v.Expect)
			}
		}
	})

	// A file with only accepting cases would pass against a verifier that returns true, and one with
	// only rejecting cases would pass against a verifier that returns false. Neither is hypothetical:
	// both are what a half-finished mirror looks like.
	t.Run("the file itself discriminates", func(t *testing.T) {
		var accept, reject int
		for _, v := range decodeVectors[verifyVector](t, "verify") {
			if v.Expect {
				accept++
			} else {
				reject++
			}
		}
		if accept == 0 || reject == 0 {
			t.Fatalf("verify vectors: %d accepting, %d rejecting — a section that is all one answer "+
				"is passed by a constant", accept, reject)
		}
	})
}

// TestRecoveryAlphabetIsAscii is what lets the DPC use the JCE's PBKDF2 at all.
//
// The password reaches a JCE provider as a char[], and providers disagree about how they turn one
// into bytes: the JDK's encodes UTF-8, and Bouncy Castle's PKCS#5 v2 scheme has historically taken
// the low byte of each character. Go encodes UTF-8. The two agree for every ASCII character and may
// differ above it — so as long as every character a generated code can contain is ASCII, the
// question cannot arise. This test is the thing that keeps that true: adding a character above
// U+007F to recoveryAlphabet turns it red here, in the repository that owns the alphabet, rather
// than on a phone.
//
// It also asserts the exclusions, because they are a product decision (a code is read aloud) that a
// tidying edit would otherwise undo without any test noticing.
func TestRecoveryAlphabetIsAscii(t *testing.T) {
	if len(recoveryAlphabet) == 0 {
		t.Fatal("recoveryAlphabet is empty")
	}
	seen := map[rune]bool{}
	for _, r := range recoveryAlphabet {
		if r > unicode.MaxASCII {
			t.Fatalf("recoveryAlphabet contains %q (U+%04X), which is not ASCII: the DPC's JCE "+
				"provider and Go may encode it differently, and the phone would then reject a "+
				"code the console accepted", r, r)
		}
		if seen[r] {
			t.Fatalf("recoveryAlphabet contains %q twice, which skews the draw", r)
		}
		seen[r] = true
		if isRecoverySeparator(r) {
			t.Fatalf("recoveryAlphabet contains %q, which normalisation strips — a code containing "+
				"it could never be typed back in", r)
		}
	}
	for _, excluded := range "ILOU01" {
		if seen[excluded] {
			t.Errorf("recoveryAlphabet contains %q; the exclusions exist so a code read aloud does "+
				"not depend on telling O from 0", excluded)
		}
	}
}
