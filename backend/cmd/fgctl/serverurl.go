package main

import (
	"fmt"
	"net"
	"net/url"
)

// requireSafeServerURL refuses a server address that would send the API key in cleartext.
//
// Every request carries the key, and `fgctl self-update` trusts the checksums the server publishes
// because the connection to it is TLS — there is no separate signature (fgctldist). An http://
// address to anything but this machine hands both to whoever is on the path, and nothing about a
// working CLI would show it: every command succeeds. Loopback is allowed, because nothing there
// crosses a wire and it is where a development server and the e2e suite listen.
func requireSafeServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Hostname() == "" {
		return fmt.Errorf("%q is not a server address; use the form https://guard.example.com", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return fmt.Errorf("%q is not https: the API key would cross the network in cleartext "+
			"(plain http is accepted only for this machine, e.g. http://127.0.0.1:8080)", raw)
	default:
		return fmt.Errorf("%q is not https; use the form https://guard.example.com", raw)
	}
}
