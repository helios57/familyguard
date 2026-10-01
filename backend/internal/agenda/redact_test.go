package agenda

import (
	"errors"
	"strings"
	"testing"
)

// A calendar's address is a capability — Google's and iCloud's private .ics links carry the secret
// in the path — and it was reaching the parent's console and the logs inside the transport's own
// error text. The error keeps the host, which is what a parent needs to tell which calendar failed.
func TestACalendarErrorNamesTheHostAndNotTheSecretAddress(t *testing.T) {
	address := "https://calendar.example.com/calendar/ical/me%40example.com/private-5f2b9c0d1e/basic.ics"
	err := errors.New(`Get "` + address + `": dial tcp: lookup calendar.example.com: no such host`)
	got := redact(err, address)
	if strings.Contains(got, "private-5f2b9c0d1e") {
		t.Fatalf("the secret path survived: %s", got)
	}
	if !strings.Contains(got, "calendar.example.com") || !strings.Contains(got, "no such host") {
		t.Fatalf("the error lost what a parent needs: %s", got)
	}
}
