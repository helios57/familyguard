package httpapi

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

// ---- learning apps (FR-30.1) -----------------------------------------------
//
// One family-wide list of package-name prefixes. A package whose name starts with one is a learning
// app on every phone of the family: always free, and its minutes earn Bonuszeit. A family setting
// rather than a per-child rule, so an app published under the prefix next month earns from its first
// minute on every child with nothing set per child.
//
// Admin-only, both ways. Writing it decides how every child earns time; reading it lists apps, and
// the guardian window lists no apps (FR-20.1).

const (
	maxLearningPrefixes      = 10
	maxLearningPrefixLength  = 100
	learningPrefixProblemMsg = "each prefix must be the start of an Android package name, for example org.example.learn. " +
		"— letters, digits and _ in dot-separated parts, at most 100 characters — and there may be at most 10"
)

// learningPrefixShape is the start of a package name: parts of letters, digits and _ that begin with
// a letter, joined by single dots, optionally ending in one. "org..x" and "org.*" name nothing a
// phone can have installed, so a row holding one would look like a setting and match no app.
var learningPrefixShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)*\.?$`)

type learningPrefixesRequest struct {
	Prefixes []string `json:"prefixes"`
}

// cleanLearningPrefixes trims and deduplicates, keeping the order given, and reports whether every
// entry is acceptable.
func cleanLearningPrefixes(in []string) ([]string, bool) {
	out := []string{}
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if len(p) > maxLearningPrefixLength || !learningPrefixShape.MatchString(p) {
			return nil, false
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, len(out) <= maxLearningPrefixes
}

func (s *Server) learningPrefixesView(c *gin.Context, prefixes []string) {
	matches, err := s.store.LearningPrefixMatches(c.Request.Context(), prefixes)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"prefixes": prefixes, "matches": matches})
}

func (s *Server) getLearningPrefixes(c *gin.Context) {
	prefixes, err := s.store.LearningPrefixes(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	s.learningPrefixesView(c, prefixes)
}

func (s *Server) putLearningPrefixes(c *gin.Context) {
	var req learningPrefixesRequest
	if !bindJSON(c, &req) {
		return
	}
	prefixes, ok := cleanLearningPrefixes(req.Prefixes)
	if !ok {
		failWith(c, http.StatusBadRequest, "invalid_input", learningPrefixProblemMsg)
		return
	}
	if err := s.store.SetLearningPrefixes(c.Request.Context(), prefixes); err != nil {
		s.fail(c, err)
		return
	}
	// The store bumped every policy version with the write; this is the fast path to the phones.
	s.blocklistChanged(c, "LEARNING_APPS_UPDATED", map[string]any{"prefixes": prefixes})
	s.learningPrefixesView(c, prefixes)
}
