package httpapi

import "testing"

func TestSignInLabelNamesTheBrowserAndSystem(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Linux; Android 13; SM-G981B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36":                     "Chrome · Android",
		"Mozilla/5.0 (Linux; Android 13; SM-G981B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/28.0 Chrome/130.0.0.0 Mobile Safari/537.36": "Samsung Internet · Android",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1":    "Safari · iPhone",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0":              "Edge · Windows",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:144.0) Gecko/20100101 Firefox/144.0":                                                       "Firefox · macOS",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/155.0.0.0 Safari/537.36":                              "Chrome · Linux",
		"curl/8.5.0":         "curl",
		"Go-http-client/1.1": "Go-http-client",
		"":                   "Unbekanntes Gerät",
		"   ":                "Unbekanntes Gerät",
	}
	for ua, want := range cases {
		if got := signInLabel(ua); got != want {
			t.Errorf("signInLabel(%q) = %q, want %q", ua, got, want)
		}
	}
	long := ""
	for range 100 {
		long += "x"
	}
	if got := signInLabel(long); len([]rune(got)) != 40 {
		t.Errorf("a 100-character product became a %d-character label", len([]rune(got)))
	}
}
