package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/config"
	"github.com/helios57/familyguard/backend/internal/store"
)

// routedServer builds the real router with no database: registration touches no store, so the
// route table and the role declarations can be read without one.
func routedServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	s := &Server{cfg: &config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r, err := s.Router()
	if err != nil {
		t.Fatalf("Router: %v", err)
	}
	return s, r
}

// onParentSurface is every /api/v1 route that authenticates as a parent. The device surface, the
// sign-in routes and enrolment authenticate some other way.
func onParentSurface(path string) bool {
	return strings.HasPrefix(path, "/api/v1/") &&
		!strings.HasPrefix(path, "/api/v1/device/") &&
		!strings.HasPrefix(path, "/api/v1/auth/") &&
		path != "/api/v1/enroll"
}

// FR-20.1: a parent route registered without saying who may call it fails here, whatever it does.
func TestEveryParentRouteDeclaresWhoMayCallIt(t *testing.T) {
	s, r := routedServer(t)
	seen := 0
	for _, rt := range r.Routes() {
		if !onParentSurface(rt.Path) {
			continue
		}
		seen++
		if _, ok := s.parentRouteRoles[routeKey{rt.Method, rt.Path}]; !ok {
			t.Errorf("%s %s is on the parent surface but declares no roles", rt.Method, rt.Path)
		}
	}
	// Positive control: a walk that matched nothing would pass the loop above vacuously.
	if seen < 45 {
		t.Fatalf("the walk saw %d parent routes; the parent surface has more than 45", seen)
	}
	if seen != len(s.parentRouteRoles) {
		t.Errorf("the router has %d parent routes but %d declarations", seen, len(s.parentRouteRoles))
	}
}

// guardianAllowlist is the whole of what a GUARDIAN may call in phase 1 (spec §2): read the
// profiles and today's state, give time today, and receive the live-update stream.
var guardianAllowlist = map[routeKey]bool{
	{http.MethodGet, "/api/v1/me"}:                                 true,
	{http.MethodGet, "/api/v1/family"}:                             true,
	{http.MethodGet, "/api/v1/children"}:                           true,
	{http.MethodPost, "/api/v1/children/:id/bonus"}:                true,
	{http.MethodPost, "/api/v1/children/:id/pause"}:                true,
	{http.MethodGet, "/api/v1/children/:id/today"}:                 true,
	{http.MethodPost, "/api/v1/children/:id/tasks/:task/decision"}: true,
	// FR-28: answering "Mehr Zeit erbitten", and the guardian's own browser hearing of it.
	{http.MethodPost, "/api/v1/children/:id/time-requests/:request/decision"}: true,
	{http.MethodGet, "/api/v1/push/key"}:                                      true,
	{http.MethodPut, "/api/v1/push/subscription"}:                             true,
	{http.MethodPost, "/api/v1/push/subscription/status"}:                     true,
	{http.MethodDelete, "/api/v1/push/subscription"}:                          true,
	{http.MethodGet, "/api/v1/devices"}:                                       true,
	{http.MethodGet, "/api/v1/devices/:id/desired-state"}:                     true,
	{http.MethodGet, "/api/v1/devices/:id/live"}:                              true,
	{http.MethodPost, "/api/v1/devices/:id/live"}:                             true,
	{http.MethodDelete, "/api/v1/devices/:id/live"}:                           true,
	{http.MethodGet, "/api/v1/events"}:                                        true,
}

func TestAGuardianMayCallExactlyTheGuardianAllowlist(t *testing.T) {
	s, _ := routedServer(t)
	for k, who := range s.parentRouteRoles {
		if got := slices.Contains(who, store.RoleGuardian); got != guardianAllowlist[k] {
			t.Errorf("%s %s: a guardian may call it = %v, want %v", k.Method, k.Path, got, guardianAllowlist[k])
		}
	}
	for k := range guardianAllowlist {
		if _, ok := s.parentRouteRoles[k]; !ok {
			t.Errorf("the allowlist names %s %s, which is not a route", k.Method, k.Path)
		}
	}
}

// The declaration is only worth something if registering through parentRoutes actually installs
// the check. Both outcomes per role, so a wrapper that always allowed or always refused fails.
func TestParentRoutesInstallTheRoleCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		role string
		who  roleSet
		want int
	}{
		{store.RoleGuardian, admins, http.StatusForbidden},
		{store.RoleGuardian, everyone, http.StatusOK},
		{store.RoleAdmin, admins, http.StatusOK},
		{store.RoleAdmin, primaryOnly, http.StatusForbidden},
		{store.RolePrimaryAdmin, primaryOnly, http.StatusOK},
	} {
		s := &Server{parentRouteRoles: map[routeKey]roleSet{}}
		r := gin.New()
		g := r.Group("/api/v1", func(c *gin.Context) {
			c.Set(ctxParent, &store.Parent{Role: tc.role})
			c.Next()
		})
		parentRoutes{s: s, group: g}.GET("/x", tc.who, func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil))
		if w.Code != tc.want {
			t.Errorf("%s calling a route for %v: got %d, want %d", tc.role, tc.who, w.Code, tc.want)
		}
		if _, ok := s.parentRouteRoles[routeKey{http.MethodGet, "/api/v1/x"}]; !ok {
			t.Errorf("the route was not recorded under its full path")
		}
	}
}
