package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"path"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/helios57/familyguard/backend/internal/config"
	"github.com/helios57/familyguard/backend/internal/store"
)

// roleSet is who may call one parent route (FR-20.1).
//
// Until phase 1 the parent surface was open to every role and seven routes opted OUT with
// requireRole, so a GUARDIAN could delete a child. Now every route opts IN: parentRoutes has no way
// to register a route without a roleSet, and TestEveryParentRouteDeclaresWhoMayCallIt fails on a
// route that went around it.
type roleSet []string

var (
	primaryOnly = roleSet{store.RolePrimaryAdmin}
	admins      = roleSet{store.RolePrimaryAdmin, store.RoleAdmin}
	everyone    = roleSet{store.RolePrimaryAdmin, store.RoleAdmin, store.RoleGuardian}
)

// routeKey names a route the way gin's route table does: method and full path pattern.
type routeKey struct{ Method, Path string }

// parentRoutes registers parent routes, records who may call each, and installs the check.
type parentRoutes struct {
	s     *Server
	group *gin.RouterGroup
}

func (pr parentRoutes) handle(method, relative string, who roleSet, handlers ...gin.HandlerFunc) {
	if len(who) == 0 {
		panic("httpapi: parent route " + method + " " + relative + " declares no roles")
	}
	full := pr.group.BasePath()
	if relative != "" {
		full = path.Join(full, relative)
	}
	pr.s.parentRouteRoles[routeKey{method, full}] = who
	pr.group.Handle(method, relative, append([]gin.HandlerFunc{pr.s.requireRole(who...)}, handlers...)...)
}

// interactive registers a route that a signed-in parent may call and an API key never may (FR-17):
// creating or removing people and credentials. Recorded, so the route table can say so to the
// tools that read it (ParentRouteTable).
func (pr parentRoutes) interactive(method, p string, who roleSet, h gin.HandlerFunc) {
	pr.handle(method, p, who, pr.s.requireInteractiveParent(), h)
	pr.s.interactiveRoutes[routeKey{method, path.Join(pr.group.BasePath(), p)}] = true
}

func (pr parentRoutes) GET(p string, who roleSet, h ...gin.HandlerFunc) {
	pr.handle(http.MethodGet, p, who, h...)
}
func (pr parentRoutes) POST(p string, who roleSet, h ...gin.HandlerFunc) {
	pr.handle(http.MethodPost, p, who, h...)
}
func (pr parentRoutes) PUT(p string, who roleSet, h ...gin.HandlerFunc) {
	pr.handle(http.MethodPut, p, who, h...)
}
func (pr parentRoutes) PATCH(p string, who roleSet, h ...gin.HandlerFunc) {
	pr.handle(http.MethodPatch, p, who, h...)
}
func (pr parentRoutes) DELETE(p string, who roleSet, h ...gin.HandlerFunc) {
	pr.handle(http.MethodDelete, p, who, h...)
}

// ParentRoute is one route of the parent surface as the router registers it.
type ParentRoute struct {
	Method, Path string
	Roles        []string
	// APIKeyAllowed is false for the routes only a signed-in parent may call (FR-17).
	APIKeyAllowed bool
}

// ParentRouteTable is the parent surface, read from the real router without a database:
// registration touches no store. For the fgctl MCP ratchet, which holds every route to a tool or to
// a written reason why not.
func ParentRouteTable() ([]ParentRoute, error) {
	s := &Server{cfg: &config.Config{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := s.Router(); err != nil {
		return nil, err
	}
	out := make([]ParentRoute, 0, len(s.parentRouteRoles))
	for k, who := range s.parentRouteRoles {
		out = append(out, ParentRoute{Method: k.Method, Path: k.Path, Roles: append([]string(nil), who...),
			APIKeyAllowed: !s.interactiveRoutes[k]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}
