package main

import "testing"

// TestServerURLMustBeTLSUnlessLoopback: every request carries the API key, and self-update trusts
// the server's checksums because the connection is TLS (fgctldist). A cleartext address anywhere
// but this machine hands both to the network. Loopback stays allowed: the e2e suite and a local
// development server listen there, and nothing crosses a wire.
func TestServerURLMustBeTLSUnlessLoopback(t *testing.T) {
	for _, ok := range []string{
		"https://guard.example.com",
		"https://guard.example.com:8443/",
		"http://127.0.0.1:8080",
		"http://127.12.0.3",
		"http://localhost:8080",
		"http://[::1]:8080",
	} {
		if err := requireSafeServerURL(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://guard.example.com",
		"http://198.51.100.10:8080", // TEST-NET-2: a LAN-shaped address that is not this machine
		"http://localhost.evil.example",
		"http://127.0.0.1.evil.example",
		"ftp://guard.example.com",
		"guard.example.com",
		"https://",
		"",
	} {
		if err := requireSafeServerURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
