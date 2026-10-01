package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestQueryIntClampsListSizesRatherThanRefusing pins a decision the code made silently: every caller
// passes a list size (limit, days, hours), and for a size the useful answer to "?limit=100000" is
// the most the server gives, not a 400 the console would have to handle. A value that is not a
// non-negative whole number is ignored for the default for the same reason.
func TestQueryIntClampsListSizesRatherThanRefusing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", 50},
		{"limit=20", 20},
		{"limit=1", 1},
		{"limit=0", 1},
		{"limit=500", 500},
		{"limit=501", 500},
		{"limit=99999999999999999999999", 500},
		{"limit=-5", 50},
		{"limit=12x", 50},
		{"limit=%20", 50},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/x?"+tc.query, nil)
		if got := queryInt(c, "limit", 50, 1, 500); got != tc.want {
			t.Errorf("?%s gave %d, want %d", tc.query, got, tc.want)
		}
	}
}
