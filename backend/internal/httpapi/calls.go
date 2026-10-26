package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/helios57/familyguard/backend/internal/store"

	"github.com/gin-gonic/gin"
)

// ---- only contacts may call (FR-31) ----------------------------------------
//
// The switch itself is a policy field (patchPolicy). Here: the family numbers that always get
// through, the calls a phone reports it rejected, and the parent's view of them.

const (
	maxFamilyNumbers       = 10
	familyNumberProblemMsg = "each number must be a phone number — +41 79 123 45 67, 0041 79 123 45 67 or " +
		"079 123 45 67 (read as Switzerland) — with 8 to 15 digits, and there may be at most 10"
	maxRejectedCallsPerReport = 100
	maxRejectedCallNumber     = 64
)

// normalizeFamilyNumber turns what a parent types into E.164: spaces, dashes, dots and parentheses
// go, a leading 00 becomes +, and a single leading 0 is a Swiss national number (the family has no
// country of its own, and the only family this serves is Swiss). Anything else — letters, an
// extension, too few or too many digits — is refused rather than guessed at, because a family
// number stored wrong is a parent whose call the phone rejects.
func normalizeFamilyNumber(raw string) (string, bool) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')' || r == '/':
		default:
			return "", false
		}
	}
	n := b.String()
	switch {
	case strings.HasPrefix(n, "+"):
	case strings.HasPrefix(n, "00"):
		n = "+" + n[2:]
	case strings.HasPrefix(n, "0"):
		n = "+41" + n[1:]
	default:
		return "", false
	}
	digits := n[1:]
	if len(digits) < 8 || len(digits) > 15 || digits[0] == '0' {
		return "", false
	}
	return n, true
}

type familyNumbersRequest struct {
	Numbers []string `json:"numbers"`
}

func (s *Server) getFamilyNumbers(c *gin.Context) {
	numbers, err := s.store.FamilyNumbers(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"numbers": numbers})
}

func (s *Server) putFamilyNumbers(c *gin.Context) {
	var req familyNumbersRequest
	if !bindJSON(c, &req) {
		return
	}
	numbers := []string{}
	for _, raw := range req.Numbers {
		n, ok := normalizeFamilyNumber(raw)
		if !ok {
			failWith(c, http.StatusBadRequest, "invalid_input", familyNumberProblemMsg)
			return
		}
		if !slices.Contains(numbers, n) {
			numbers = append(numbers, n)
		}
	}
	if len(numbers) > maxFamilyNumbers {
		failWith(c, http.StatusBadRequest, "invalid_input", familyNumberProblemMsg)
		return
	}
	if err := s.store.SetFamilyNumbers(c.Request.Context(), numbers); err != nil {
		s.fail(c, err)
		return
	}
	// The store bumped every policy version with the write; this is the fast path to the phones.
	s.blocklistChanged(c, "FAMILY_NUMBERS_UPDATED", map[string]any{"count": len(numbers)})
	c.JSON(http.StatusOK, gin.H{"numbers": numbers})
}

type rejectedCallReport struct {
	Calls []struct {
		ID     string `json:"id"`
		Number string `json:"number"`
		At     string `json:"at"`
	} `json:"calls"`
}

// deviceRejectedCalls stores the calls the phone rejected (FR-31.5). The phone re-sends until it has
// a 200, so a call already stored is accepted again and not counted twice.
func (s *Server) deviceRejectedCalls(c *gin.Context) {
	dev := deviceOf(c)
	var req rejectedCallReport
	if !bindJSON(c, &req) {
		return
	}
	if len(req.Calls) > maxRejectedCallsPerReport {
		failWith(c, http.StatusBadRequest, "invalid_input", "at most 100 calls per report")
		return
	}
	calls := make([]store.RejectedCall, 0, len(req.Calls))
	for _, rc := range req.Calls {
		at, err := time.Parse(time.RFC3339, rc.At)
		id := strings.TrimSpace(rc.ID)
		if err != nil || id == "" || len(id) > 64 || len(rc.Number) > maxRejectedCallNumber {
			failWith(c, http.StatusBadRequest, "invalid_input",
				"each call needs an id (at most 64 characters), a number (at most 64) and an RFC 3339 time")
			return
		}
		calls = append(calls, store.RejectedCall{ClientID: id, Number: rc.Number, RejectedAt: at})
	}
	stored, err := s.store.RecordRejectedCalls(c.Request.Context(), dev.ID, calls)
	if err != nil {
		s.fail(c, err)
		return
	}
	if stored > 0 {
		s.hub.PublishParents(Event{Type: "rejected_calls", DeviceID: dev.ID.String(), ChildID: dev.ChildID.String()})
	}
	c.JSON(http.StatusOK, gin.H{"accepted": stored})
}

// deviceRejectedCallsView is a phone's rejected calls of the last days (default 7, at most 365).
func (s *Server) deviceRejectedCallsView(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	days := queryInt(c, "days", 7, 1, 365)
	calls, err := s.store.ListRejectedCalls(c.Request.Context(), id, s.now().Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"calls": calls})
}
