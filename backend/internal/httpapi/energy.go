package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/energy"
)

// deviceEnergy is what FamilyGuard spent on a phone per hour, as the phone measured it (FR-26.5).
//
// The window reaches one sample further back than asked, so the first interval inside it has the
// report it began at; hours before the window are then dropped rather than half-counted.
func (s *Server) deviceEnergy(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	hours := queryInt(c, "hours", 24, 1, 24*31)
	from := s.now().UTC().Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)
	samples, err := s.store.EnergySamples(c.Request.Context(), id, from.Add(-2*time.Hour))
	if err != nil {
		s.fail(c, err)
		return
	}
	all, _ := energy.Hourly(samples)
	inWindow := []energy.Totals{}
	var total energy.Totals
	for _, h := range all {
		if h.Hour.Before(from) {
			continue
		}
		inWindow = append(inWindow, h)
		total = energy.Sum(total, h)
	}
	c.JSON(http.StatusOK, gin.H{"from": from, "hours": inWindow, "total": total, "samples": len(samples)})
}
