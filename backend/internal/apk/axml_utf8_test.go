package apk

import (
	"errors"
	"strings"
	"testing"
)

// The fixtures' manifests carry UTF-16 string pools, so the UTF-8 branch — which a build tool is
// free to choose, and which resources compiled with aapt2's defaults use — was reached by nothing.
// A label in it is read by its BYTE count, not its character count, which differ for anything past
// ASCII; and a length that runs past the buffer is refused rather than sliced.
func TestUTF8PoolStringsAreReadByTheirByteCount(t *testing.T) {
	label := "Zähne putzen 🦷"
	enc := func(n int) []byte {
		if n < 0x80 {
			return []byte{byte(n)}
		}
		return []byte{byte(0x80 | n>>8), byte(n)}
	}
	chars := len([]rune(label))
	b := append(append(enc(chars), enc(len(label))...), label...)
	got, err := readPoolString(append(b, 0), true)
	if err != nil || got != label {
		t.Fatalf("read %q, %v; want %q", got, err, label)
	}

	// A long (two-byte) length: 300 bytes of text.
	long := strings.Repeat("a", 300)
	b = append(append(enc(300), enc(300)...), long...)
	if got, err := readPoolString(b, true); err != nil || got != long {
		t.Fatalf("a 300-byte string read as %d bytes, %v", len(got), err)
	}

	for name, bad := range map[string][]byte{
		"no length at all":            {},
		"a truncated long length":     {0x81},
		"a byte count past the end":   {3, 10, 'a', 'b', 'c'},
		"a truncated long byte count": {3, 0x80},
	} {
		if _, err := readPoolString(bad, true); !errors.Is(err, ErrNotBinaryXML) {
			t.Errorf("%s: err %v, want ErrNotBinaryXML", name, err)
		}
	}
}
