package webpush

import "testing"

// The address is a parent's input and the request leaves the cluster: only the browsers' push
// services, over https on the default port, and a bench's own receiver when it names it.
func TestOnlyPushServicesAreAddressed(t *testing.T) {
	s := New(nil, Options{ExtraHosts: []string{"127.0.0.1:9999"}})
	for endpoint, want := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/abc":                     true,
		"https://updates.push.services.mozilla.com/wpush/v2/abc":      true,
		"https://web.push.apple.com/QGx":                              true,
		"https://wns2-db5p.notify.windows.com/w/?token=x":             true,
		"http://fcm.googleapis.com/fcm/send/abc":                      false, // not https
		"https://fcm.googleapis.com:8443/fcm/send/abc":                false, // not the default port
		"https://fcm.googleapis.com.evil.example/x":                   false, // suffix of a different name
		"https://evilpush.apple.com.example/x":                        false,
		"https://notpush.services.mozilla.com.attacker.example/x":     false,
		"https://user@fcm.googleapis.com/x":                           false, // userinfo
		"https://10.0.0.1/x":                                          false,
		"https://kubernetes.default.svc/x":                            false,
		"http://127.0.0.1:9999/push/1":                                true, // the bench's receiver
		"http://127.0.0.1:9998/push/1":                                false,
		"":                                                            false,
		"not a url":                                                   false,
		"https://xn--push-apple-com.example/x":                        false,
		"https://PUSH.SERVICES.MOZILLA.COM.example/x":                 false,
		"https://Updates.Push.Services.Mozilla.Com/wpush/v2/abc":      true,
		"https://fcm.googleapis.com/fcm/send/abc?x=1#y":               true,
		"https://apple.com/x":                                         false,
		"https://push.apple.com/x":                                    true,
		"https://a.notify.windows.com.evil/x":                         false,
		"ftp://fcm.googleapis.com/x":                                  false,
		"https://[::1]/x":                                             false,
		"https://fcm.googleapis.com./x":                               false,
		"https://fcm.googleapis.com/fcm/send/" + string(rune(0x2028)): true,
	} {
		if got := s.AllowedEndpoint(endpoint); got != want {
			t.Errorf("AllowedEndpoint(%q) = %v, want %v", endpoint, got, want)
		}
	}
}
