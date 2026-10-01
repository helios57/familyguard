package fgclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What a proxy answers instead of the server — an HTML error page, often kilobytes of it — must
// reach the CLI user as one short line that still says what happened, not as the page and not as
// a JSON decoding error that blames the wrong thing.
func TestAnErrorThatIsNotJSONIsSummarised(t *testing.T) {
	page := "<html>\n<head><title>502 Bad Gateway</title></head>\n<body>" + strings.Repeat("x", 4000) + "</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	var out map[string]any
	err := New(srv.URL, "token").Get(context.Background(), "/api/v1/me", &out)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err %v is not an APIError", err)
	}
	if apiErr.Status != http.StatusBadGateway || !strings.Contains(apiErr.Message, "502 Bad Gateway") {
		t.Errorf("status %d, message %q", apiErr.Status, apiErr.Message)
	}
	if len(apiErr.Message) > 210 || strings.Contains(apiErr.Message, "\n") {
		t.Errorf("the message is %d bytes over several lines, starting %q", len(apiErr.Message), apiErr.Message[:80])
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer empty.Close()
	err = New(empty.URL, "token").Get(context.Background(), "/api/v1/me", &out)
	if !errors.As(err, &apiErr) || apiErr.Message != "empty body" {
		t.Errorf("an empty error body reads %v", err)
	}
}
