package httpapi

import (
	"net/http"
	"path"

	"github.com/gin-gonic/gin"

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
