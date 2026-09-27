# Phase 1 — Roles and rights: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `GUARDIAN` can do only what the guardian window needs, every parent route says who may call it, a primary admin can choose and change roles in the console, and a guardian who signs in sees a guardian view instead of an admin console full of refusals.

**Architecture:**
- **Role declarations.** Parent routes are registered through a small wrapper (`parentRoutes`) whose methods take the allowed roles as a required argument and insert `requireRole` themselves. A router walk in a unit test fails on any `/api/v1` parent route that bypassed the wrapper.
- **Role changes.** A new `PATCH /parents/:id` changes a role. It is primary-admin only and console-only, like adding a parent.
- **Console.** The console gets a People & rights card. A guardian gets a German guardian view, which Phase 2 extends with pause and −time.

**Tech Stack:** Go (Gin, pgx) in `backend/`; vanilla JS console in `backend/internal/console/assets/`; black-box e2e in `tests/e2e/` (real server, real PostgreSQL, real Chrome).

**Spec:** `docs/superpowers/specs/2026-09-27-daily-plan-design.md` — sections 2 (roles), 6 (guardian window, first cut), 13 (phase 1).

## Global Constraints

- **Git:** push directly to `main`, no PR. End every commit message with `Claude-Session: https://claude.ai/code/session_01U14hzEgBumKQ6xTunjwMGa`.
- **Public repo.** Before each commit, run the internals check that is kept outside this repo (the owner's host names, addresses, storage paths and family names — never written here). It must print nothing. Never stage `playlist.md`.
- **Versions.** Always the latest version of every dependency; do not add dependencies for this phase.
- **Test integrity (NFR-12).** Every new test is shown RED on a known-bad input before its green counts:
  - Change a VALUE, never delete a symbol. A compile error is not a calibration.
  - Snapshot with `cp`, restore with `cp`, and prove the restore with `cmp`. Never use `git checkout --`: it discards uncommitted work.
  - Record each probe in the Phase 34 calibration table.
- **Deploy.** GitOps only; never `kubectl apply/edit`. An argocd commit is a deploy. Verify a deploy by digest, never by tag.
- **Secrets.** Never print a secret or a prefix of one. `apksigner` passwords only via `env:KSPASS`.
- **Copy.** The admin console stays English. The guardian view is German, because its users are the family's non-admins and the spec's copy is German.

## Review Focus

Failure modes the spec implies that are most likely to bite, each pinned by a test in the owning task:

1. **Stale link.** A guardian opens an old bookmark such as `#/rules`. They must land on the guardian view, not "Could not load this page" (Task 4, browser test).
2. **Demotion while signed in.** A parent demoted while signed in must lose the rights on their very next request, with the same session token (Task 2, e2e).
3. **Keys follow their owner's role.** An API key belongs to a parent who is later demoted to guardian. The key must lose admin rights with them, because a key is its parent (Task 2, e2e).
4. **No phone yet.** A profile with no phone set up shows "Noch kein Handy eingerichtet", not an empty card or an error (Task 4, browser test).
5. **No daily limit.** A profile with no daily limit shows no +time buttons, because the server would answer 409 `no_daily_limit` (Task 4, browser test).

---

### Task 1: Every parent route declares its roles; guardian allowlist

**Files:**
- Create: `backend/internal/httpapi/roles.go`
- Create: `backend/internal/httpapi/roles_test.go`
- Modify: `backend/internal/httpapi/server.go` (Server struct; `Router()` parent section, currently lines ~208–289)

**Interfaces:**
- Produces:
  - `type roleSet []string`; `primaryOnly`, `admins`, `everyone`.
  - `type routeKey struct{ Method, Path string }`.
  - `type parentRoutes struct{ s *Server; group *gin.RouterGroup }` with `GET/POST/PUT/PATCH/DELETE(path string, who roleSet, h ...gin.HandlerFunc)`.
  - `Server.parentRouteRoles map[routeKey]roleSet`, filled by `Router()`.

- [ ] **Step 1: Write the failing tests** — `backend/internal/httpapi/roles_test.go`:

```go
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
	{http.MethodGet, "/api/v1/me"}:                        true,
	{http.MethodGet, "/api/v1/family"}:                    true,
	{http.MethodGet, "/api/v1/children"}:                  true,
	{http.MethodPost, "/api/v1/children/:id/bonus"}:       true,
	{http.MethodGet, "/api/v1/devices"}:                   true,
	{http.MethodGet, "/api/v1/devices/:id/desired-state"}: true,
	{http.MethodGet, "/api/v1/events"}:                    true,
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
```

`routedServer` builds the router from an empty `config.Config`. If `Router()` dereferences a config field that is nil there, set that field in the literal. Do not change `Router()` to make the test build.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd backend && go test ./internal/httpapi -run 'TestEveryParentRoute|TestAGuardianMay|TestParentRoutesInstall' 2>&1 | tail -5`
Expected: build failure, `undefined: parentRoutes` / `s.parentRouteRoles`. This is the TDD red, not a calibration. Calibration follows in Step 5.

- [ ] **Step 3: Implement `roles.go`**

```go
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

func (pr parentRoutes) GET(p string, who roleSet, h ...gin.HandlerFunc)  { pr.handle(http.MethodGet, p, who, h...) }
func (pr parentRoutes) POST(p string, who roleSet, h ...gin.HandlerFunc) { pr.handle(http.MethodPost, p, who, h...) }
func (pr parentRoutes) PUT(p string, who roleSet, h ...gin.HandlerFunc)  { pr.handle(http.MethodPut, p, who, h...) }
func (pr parentRoutes) PATCH(p string, who roleSet, h ...gin.HandlerFunc) {
	pr.handle(http.MethodPatch, p, who, h...)
}
func (pr parentRoutes) DELETE(p string, who roleSet, h ...gin.HandlerFunc) {
	pr.handle(http.MethodDelete, p, who, h...)
}
```

- [ ] **Step 4: Rewire `server.go`**
  - Add `parentRouteRoles map[routeKey]roleSet` to `Server`.
  - In `Router()`, replace `p := v1.Group("", s.requireParent(), RateLimitBy(parents, parentKey))` and every `p.X(...)` / `k.X(...)` line down to `p.GET("/events", …)` with the block below.
  - Keep every existing comment above its route.
  - Delete the now-redundant `s.requireRole(...)` arguments. The wrapper installs the check, and a second one would only duplicate it.

```go
	s.parentRouteRoles = map[routeKey]roleSet{}
	p := parentRoutes{s: s, group: v1.Group("", s.requireParent(), RateLimitBy(parents, parentKey))}
	p.GET("/me", everyone, s.me)
	p.GET("/family", everyone, s.getFamily)
	p.GET("/dpc", admins, s.hostedDPC)
	p.GET("/family/blocked-packages", admins, s.listFamilyBlocklist)
	p.PUT("/family/blocked-packages", admins, s.putFamilyBlocklist)
	p.DELETE("/family/blocked-packages", admins, s.deleteFamilyBlocklist)
	p.GET("/parents", admins, s.listParents)
	p.POST("/parents", primaryOnly, s.requireInteractiveParent(), s.createParent)
	p.DELETE("/parents/:id", primaryOnly, s.requireInteractiveParent(), s.deleteParent)

	p.GET("/children", everyone, s.listChildren)
	p.POST("/children", admins, s.createChild)
	p.PATCH("/children/:id", admins, s.updateChild)
	p.DELETE("/children/:id", admins, s.deleteChild)
	p.GET("/children/:id/policy", admins, s.getPolicy)
	p.POST("/children/:id/bonus", everyone, s.grantBonus)
	p.PATCH("/children/:id/policy", admins, s.patchPolicy)
	p.GET("/children/:id/app-rules", admins, s.listAppRules)
	p.PUT("/children/:id/app-rules", admins, s.putAppRule)
	p.DELETE("/children/:id/app-rules", admins, s.deleteAppRule)
	p.GET("/children/:id/blocked-domains", admins, s.listBlockedDomains)
	p.POST("/children/:id/blocked-domains", admins, s.addBlockedDomain)
	p.DELETE("/children/:id/blocked-domains", admins, s.removeBlockedDomain)
	p.POST("/children/:id/devices", admins, s.createDevice)

	p.GET("/devices", everyone, s.listDevices)
	p.GET("/devices/:id", admins, s.getDevice)
	p.PATCH("/devices/:id", admins, s.renameDevice)
	p.DELETE("/devices/:id", admins, s.deleteDevice)
	p.POST("/devices/:id/provisioning", admins, s.provisioningPayload)
	p.GET("/devices/:id/recovery-code", admins, s.recoveryCode)
	p.GET("/devices/:id/recovery-events", admins, s.listRecoveryEvents)
	p.GET("/devices/:id/apps", admins, s.listDeviceApps)
	p.DELETE("/devices/:id/apps/:package", admins, s.forgetDeviceApp)
	p.GET("/devices/:id/usage", admins, s.deviceUsage)
	p.GET("/devices/:id/usage/timeline", admins, s.deviceUsageTimeline)
	p.GET("/devices/:id/locations", admins, s.deviceLocations)
	p.GET("/devices/:id/desired-state", everyone, s.deviceDesiredState)
	p.GET("/devices/:id/commands", admins, s.listCommands)
	p.POST("/devices/:id/commands", admins, s.createCommand)
	p.GET("/devices/:id/debug", admins, s.openDebugStream)
	p.GET("/apps", admins, s.listApps)
	p.POST("/apps", admins, s.uploadApp)
	p.POST("/apps/scan", admins, s.scanApps)
	p.DELETE("/apps/:id", admins, s.deleteApp)
	p.GET("/children/:id/managed-apps", admins, s.listManagedApps)
	p.PUT("/children/:id/managed-apps/:package", admins, s.declareManagedApp)
	p.DELETE("/children/:id/managed-apps/:package", admins, s.withdrawManagedApp)

	p.GET("/api-keys", primaryOnly, s.listAPIKeys)
	p.POST("/api-keys", primaryOnly, s.requireInteractiveParent(), s.createAPIKey)
	p.POST("/api-keys/:id/revoke", primaryOnly, s.requireInteractiveParent(), s.revokeAPIKey)
	p.DELETE("/api-keys/:id", primaryOnly, s.requireInteractiveParent(), s.deleteAPIKey)

	p.GET("/audit", admins, s.listAudit)
	p.GET("/events", everyone, s.parentEvents)
```

  Keep the FR-18 comment on the blocklist and add one line to it: reading the blocklist moved from every parent to admins in FR-20, because the guardian window lists no apps.

- [ ] **Step 5: Run the tests and calibrate each one red.** Take a snapshot first: `cp backend/internal/httpapi/server.go $SP/server.go.bak && cp backend/internal/httpapi/roles.go $SP/roles.go.bak`. `$SP` is the session scratchpad.

Run: `cd backend && go test ./internal/httpapi -run 'TestEveryParentRoute|TestAGuardianMay|TestParentRoutesInstall' -v 2>&1 | grep -E '^(=== RUN|--- |ok|FAIL)'`
Expected: 3 PASS. Then take each probe below, confirm the named test goes red with its OWN assertion message, and restore:

| # | Probe (value change only) | Test that must go red | Message to grep |
|---|---|---|---|
| 1 | Register `/audit` on the raw group: `p.group.GET("/audit", s.listAudit)` instead of `p.GET("/audit", admins, s.listAudit)` | TestEveryParentRouteDeclaresWhoMayCallIt | `declares no roles` |
| 2 | `/children/:id/policy` `admins` → `everyone` | TestAGuardianMayCallExactlyTheGuardianAllowlist | `a guardian may call it = true, want false` |
| 3 | In `handle`, pass `handlers...` without the prepended `requireRole` | TestParentRoutesInstallTheRoleCheck | `got 200, want 403` |
| 4 | In `onParentSurface`, return `false` unconditionally | TestEveryParentRouteDeclaresWhoMayCallIt | `the walk saw 0 parent routes` |

After each probe: `cp $SP/<file>.bak <file>` and then `cmp` the two. A probe that stays green is recorded as such, with the reason; it is not counted.

- [ ] **Step 6: Full backend sweep**

Run: `cd backend && go build ./... && go vet ./... && go test ./... 2>&1 | tail -15; d=$(gofmt -l .); [ -z "$d" ] || echo "GOFMT DIRTY: $d"`
Expected: all `ok`, no GOFMT line.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/httpapi/roles.go backend/internal/httpapi/roles_test.go backend/internal/httpapi/server.go
# run the internals check (Global Constraints) on the staged diff before committing
git commit -m "roles: every parent route says who may call it; a guardian gets the guardian window only (FR-20.1)" -m "<why + calibration probes 1-4 and their results>" -m "Claude-Session: https://claude.ai/code/session_01U14hzEgBumKQ6xTunjwMGa"
```

---

### Task 2: Change a role (`PATCH /parents/:id`)

**Files:**
- Modify: `backend/internal/store/family.go` (add `UpdateParentRole` after `DeleteParent`)
- Modify: `backend/internal/httpapi/parents.go` (add `validRole`, `updateParentRole`; use `validRole` in `createParent`)
- Modify: `backend/internal/httpapi/server.go` (one route)
- Create: `tests/e2e/roles_test.go`
- Modify: `tests/e2e/audit_test.go` (PARENT_ROLE_CHANGED)

**Interfaces:**
- Consumes: `parentRoutes`, `primaryOnly` (Task 1).
- Produces:
  - `func (s *Store) UpdateParentRole(ctx context.Context, id uuid.UUID, role string) (previous string, err error)`.
  - `PATCH /api/v1/parents/:id` with body `{"role": "..."}`. It returns 200 and the parent.
  - The audit action `PARENT_ROLE_CHANGED`, with detail `{"from": ..., "to": ...}`.

- [ ] **Step 1: Write the failing e2e test** — `tests/e2e/roles_test.go`:

```go
package e2e

// FR-20: who may do what, measured against the real server. The unit tests in httpapi prove every
// route DECLARES its roles; these prove the declarations are what a signed-in person actually meets.

import (
	"net/http"
	"testing"
)

var guardianIdentity = identity{
	Email: "guardian@family.test", Subject: "google-guardian", Name: "Guardian", Verified: true,
}

func (h *harness) addParent(token, email, role string) parentDTO {
	h.t.Helper()
	var p parentDTO
	h.call(http.MethodPost, "/parents", token, map[string]any{"email": email, "role": role}).
		expect(http.StatusCreated).decode(&p)
	return p
}

func (h *harness) setRole(token, parentID, role string) apiResponse {
	return h.call(http.MethodPatch, "/parents/"+parentID, token, map[string]any{"role": role})
}

func TestARoleChangeTakesEffectOnTheNextRequest(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	added := h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	limit := map[string]any{"daily_limit_minutes": 60}
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", guardian.Token, limit).
		expectError(http.StatusForbidden, "forbidden")

	var promoted parentDTO
	h.setRole(primary.Token, added.ID, "ADMIN").expect(http.StatusOK).decode(&promoted)
	if promoted.Role != "ADMIN" {
		t.Fatalf("the answer says %q, expected ADMIN", promoted.Role)
	}
	// The same session token, not a new sign-in: the role is read from the database per request.
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", guardian.Token, limit).expect(http.StatusOK)

	h.setRole(primary.Token, added.ID, "GUARDIAN").expect(http.StatusOK)
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", guardian.Token, limit).
		expectError(http.StatusForbidden, "forbidden")
	h.call(http.MethodGet, "/children", guardian.Token, nil).expect(http.StatusOK)
}

// Review focus 3: a key is its parent, so a demoted parent's key is demoted with them.
func TestADemotedParentsKeyIsDemotedWithThem(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	child := h.newChild(primary.Token, "Mira")
	var second parentDTO
	h.call(http.MethodGet, "/me", h.signIn(secondParent).Token, nil).expect(http.StatusOK).decode(&second)
	h.setRole(primary.Token, second.ID, "PRIMARY_ADMIN").expect(http.StatusOK)
	secondSession := h.signIn(secondParent)

	var key struct {
		Token string `json:"token"`
	}
	h.call(http.MethodPost, "/api-keys", secondSession.Token, map[string]any{"name": "laptop"}).
		expect(http.StatusCreated).decode(&key)
	limit := map[string]any{"daily_limit_minutes": 45}
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", key.Token, limit).expect(http.StatusOK)

	h.setRole(primary.Token, second.ID, "GUARDIAN").expect(http.StatusOK)
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", key.Token, limit).
		expectError(http.StatusForbidden, "forbidden")
	h.call(http.MethodGet, "/children", key.Token, nil).expect(http.StatusOK)
}

func TestOnlyThePrimaryAdminChangesRolesAndNeverTheirOwn(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	admin := h.signIn(secondParent)
	added := h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	var key struct {
		Token string `json:"token"`
	}
	h.call(http.MethodPost, "/api-keys", primary.Token, map[string]any{"name": "mcp"}).
		expect(http.StatusCreated).decode(&key)

	h.mustRefuse(t, []refusal{
		{what: "an admin changing a role", method: http.MethodPatch, path: "/parents/" + added.ID,
			token: admin.Token, body: map[string]any{"role": "ADMIN"},
			status: http.StatusForbidden, code: "forbidden"},
		{what: "an API key changing a role", method: http.MethodPatch, path: "/parents/" + added.ID,
			token: key.Token, body: map[string]any{"role": "ADMIN"},
			status: http.StatusForbidden, code: "api_key_forbidden"},
		{what: "the primary admin changing their own role", method: http.MethodPatch,
			path: "/parents/" + primary.Parent.ID, token: primary.Token, body: map[string]any{"role": "ADMIN"},
			status: http.StatusConflict, code: "conflict", says: []string{"your own role"}},
		{what: "a role that does not exist", method: http.MethodPatch, path: "/parents/" + added.ID,
			token: primary.Token, body: map[string]any{"role": "SUPERUSER"},
			status: http.StatusBadRequest, code: "invalid_input", says: []string{"PRIMARY_ADMIN", "ADMIN", "GUARDIAN"}},
		{what: "a parent who does not exist", method: http.MethodPatch,
			path: "/parents/11111111-2222-3333-4444-555555555555", token: primary.Token,
			body: map[string]any{"role": "ADMIN"}, status: http.StatusNotFound, code: "not_found"},
	})
	// The refusals above are about who asked: the same change from the primary admin lands.
	h.setRole(primary.Token, added.ID, "ADMIN").expect(http.StatusOK)
}

// Self-change is refused, so the last-primary rule can only be reached by two primary admins
// demoting each other at once. Both requests run concurrently, round after round; after each round
// at least one primary admin must remain.
func TestTwoPrimaryAdminsCannotDemoteEachOtherToNone(t *testing.T) {
	h := newHarness(t)
	a := h.signIn(primaryParent)
	var bID string
	{
		var me parentDTO
		h.call(http.MethodGet, "/me", h.signIn(secondParent).Token, nil).expect(http.StatusOK).decode(&me)
		bID = me.ID
	}
	h.setRole(a.Token, bID, "PRIMARY_ADMIN").expect(http.StatusOK)
	for round := 0; round < 20; round++ {
		b := h.signIn(secondParent)
		done := make(chan int, 2)
		go func() { done <- h.setRole(a.Token, bID, "ADMIN").Status }()
		go func() { done <- h.setRole(b.Token, a.Parent.ID, "ADMIN").Status }()
		<-done
		<-done

		// Both are at least ADMIN whatever happened, so either may read the list.
		var list struct {
			Parents []parentDTO `json:"parents"`
		}
		h.call(http.MethodGet, "/parents", a.Token, nil).expect(http.StatusOK).decode(&list)
		var survivor string
		for _, p := range list.Parents {
			if p.Role == "PRIMARY_ADMIN" && (p.ID == a.Parent.ID || p.ID == bID) {
				survivor = p.ID
			}
		}
		if survivor == "" {
			t.Fatalf("round %d: neither primary admin is one any more", round)
		}
		// Start the next round from two primary admins again: the survivor promotes the other.
		if survivor == a.Parent.ID {
			h.setRole(a.Token, bID, "PRIMARY_ADMIN").expect(http.StatusOK)
		} else {
			h.setRole(b.Token, a.Parent.ID, "PRIMARY_ADMIN").expect(http.StatusOK)
		}
	}
}
```

Also in `tests/e2e/audit_test.go`:
- Right after `byParent("PARENT_ADDED", "parent", added.ID)`, add:
  ```go
  h.call(http.MethodPatch, "/parents/"+added.ID, parent.Token, map[string]any{"role": "ADMIN"}).expect(http.StatusOK)
  byParent("PARENT_ROLE_CHANGED", "parent", added.ID)
  ```
- In the detail table, after the `{"PARENT_ADDED", "email", "third@family.test"}` row, add `{"PARENT_ROLE_CHANGED", "to", "ADMIN"},` and `{"PARENT_ROLE_CHANGED", "from", "GUARDIAN"},`. If the table allows one row per action, keep only the `to` row and assert `from` inline next to the `byParent` call.
- Fix the count in the file's header comment ("all twenty-one actions") to the new number.

- [ ] **Step 2: Run to verify it fails**

Run: `bash tests/e2e/run.sh -run 'TestARoleChange|TestADemoted|TestOnlyThePrimary|TestTwoPrimary|TestAudit' 2>&1 | tail -30` (check `run.sh` for how it forwards `-run`; if it does not, set `E2E_RUN` as it documents).
Expected: FAIL. PATCH `/parents/:id` answers 404 `not_found` (no route), so each test fails on its first `setRole`. `rc=1`, not 2: an `rc=2` means the harness did not run, which is not a red.

- [ ] **Step 3: Implement the store function** — in `backend/internal/store/family.go`, after `DeleteParent`:

```go
// UpdateParentRole changes a parent's role and returns the one it replaced. Demoting the last
// PRIMARY_ADMIN is refused inside the same transaction, as DeleteParent refuses removing one. Every
// primary admin row is locked first, so two primary admins demoting each other at the same moment
// are serialised and the second sees the first's result.
func (s *Store) UpdateParentRole(ctx context.Context, id uuid.UUID, role string) (string, error) {
	var previous string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM parents WHERE role = $1 FOR UPDATE`, RolePrimaryAdmin); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT role FROM parents WHERE id = $1 FOR UPDATE`, id).Scan(&previous); err != nil {
			return mapErr(err)
		}
		if previous == RolePrimaryAdmin && role != RolePrimaryAdmin {
			var remaining int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM parents WHERE role = $1 AND id <> $2`, RolePrimaryAdmin, id).
				Scan(&remaining); err != nil {
				return err
			}
			if remaining == 0 {
				return fmt.Errorf("%w: cannot demote the last primary admin", ErrConflict)
			}
		}
		_, err := tx.Exec(ctx, `UPDATE parents SET role = $2 WHERE id = $1`, id, role)
		return err
	})
	return previous, err
}
```

- [ ] **Step 4: Implement the handler** — in `backend/internal/httpapi/parents.go`:

```go
// validRole is the one list of roles a request may name.
func validRole(role string) bool {
	switch role {
	case store.RolePrimaryAdmin, store.RoleAdmin, store.RoleGuardian:
		return true
	}
	return false
}

type updateParentRequest struct {
	Role string `json:"role"`
}

// updateParentRole changes who a parent is (FR-20.2). Console-only and primary-admin only, like
// adding one: a role is an authority, and one that a key could grant would outlive the key.
func (s *Server) updateParentRole(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req updateParentRequest
	if !bindJSON(c, &req) {
		return
	}
	if !validRole(req.Role) {
		failWith(c, http.StatusBadRequest, "invalid_input", "role must be PRIMARY_ADMIN, ADMIN or GUARDIAN")
		return
	}
	// Refused for the same reason deleteParent refuses removing yourself: the one person who could
	// undo it is the person who just lost the right to.
	if p := parentOf(c); p != nil && p.ID == id {
		failWith(c, http.StatusConflict, "conflict", "you cannot change your own role")
		return
	}
	previous, err := s.store.UpdateParentRole(c.Request.Context(), id, req.Role)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "PARENT_ROLE_CHANGED", "parent", id.String(), map[string]any{
		"from": previous, "to": req.Role,
	})
	parent, err := s.store.ParentByID(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, parent)
}
```

In `createParent`, replace the `switch req.Role {…}` block with `if !validRole(req.Role) { failWith(c, http.StatusBadRequest, "invalid_input", "role must be PRIMARY_ADMIN, ADMIN or GUARDIAN"); return }`.

In `server.go`, after the `POST /parents` line: `p.PATCH("/parents/:id", primaryOnly, s.requireInteractiveParent(), s.updateParentRole)`.

Confirm that `s.fail` maps `store.ErrNotFound` → 404 `not_found` and `ErrConflict` → 409 `conflict`: `grep -n "func (s \*Server) fail" -A25 backend/internal/httpapi/*.go`.

- [ ] **Step 5: Run the tests to verify they pass, then calibrate.** Snapshot `family.go` and `parents.go` to `$SP`, as in Task 1.

Run: `bash tests/e2e/run.sh -run 'TestARoleChange|TestADemoted|TestOnlyThePrimary|TestTwoPrimary|TestAudit' 2>&1 | tail -30`
Expected: PASS, `rc=0`.

| # | Probe | Must go red | Message |
|---|---|---|---|
| 5 | `UPDATE parents SET role = $2` → `SET role = role` (the write lands on nothing) | TestARoleChangeTakesEffectOnTheNextRequest | `expected ADMIN` |
| 6 | the self-check `p.ID == id` → `p.ID == uuid.Nil` | TestOnlyThePrimaryAdmin… | `the primary admin changing their own role` |
| 7 | `remaining == 0` → `remaining < 0` | TestTwoPrimaryAdminsCannotDemoteEachOtherToNone | `has no primary admin left`. **Race-dependent:** record how many of 20 rounds went red. If none did, record the probe as green-by-scheduling rather than as a calibration. |
| 8 | the audit action string `"PARENT_ROLE_CHANGED"` → `"PARENT_ROLE_CHANGE"` | TestAudit… | the missing-row message for PARENT_ROLE_CHANGED |

Restore after each probe and `cmp` the restored file against its snapshot.

- [ ] **Step 6: Full backend sweep** (as in Task 1, Step 6) **plus the full e2e suite:** `bash tests/e2e/run.sh 2>&1 | tail -5`. Expected: `rc=0`. Some existing tests use a guardian for things it may no longer do, and they now fail. **Known one:** `TestTheBlocklistIsReachableByAPIKeyAndGuardedByRole` asserts "a guardian sees the list". Change that assertion to expect 403 `forbidden`, and change its comment to say a guardian's window lists no apps (FR-20). Any other red: read it, decide whether the test asserted old behaviour (fix the test) or found a real gap (fix the code), and write down which one it was.

- [ ] **Step 7: Commit** — `backend/internal/store/family.go`, `backend/internal/httpapi/parents.go`, `backend/internal/httpapi/server.go`, `tests/e2e/roles_test.go`, `tests/e2e/audit_test.go`, and `tests/e2e/blocklist_test.go`. Run the internals grep first. Message: "roles: a primary admin changes who someone is, and it holds on their next request (FR-20.2)", plus the probes and their results.

---

### Task 3: A guardian meets 403 everywhere outside the allowlist (e2e)

**Files:**
- Modify: `tests/e2e/roles_test.go` (add one test)

**Interfaces:**
- Consumes: `guardianIdentity`, `h.addParent` (Task 2); the allowlist of Task 1.

- [ ] **Step 1: Write the test**

```go
// Every parent route that is not on the guardian allowlist, called as a guardian, against a real
// server with a real enrolled phone, so a path parameter that resolves to nothing cannot be what
// produced the refusal. The unit test in httpapi is what catches a NEW route; this list is the
// surface as of phase 1.
func TestAGuardianIsRefusedEverythingOutsideTheGuardianWindow(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	admin := h.signIn(secondParent)
	child := h.newChild(primary.Token, "Mira")
	device := h.newDevice(primary.Token, child.ID, "Mira's phone")
	_, enrollToken := h.provision(primary.Token, device.ID)
	h.enrollDevice(enrollToken, "Pixel 8", "Android 16", nil)
	h.call(http.MethodPatch, "/children/"+child.ID+"/policy", primary.Token,
		map[string]any{"daily_limit_minutes": 60}).expect(http.StatusOK)
	h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")
	guardian := h.signIn(guardianIdentity)

	c, d := "/children/"+child.ID, "/devices/"+device.ID
	allowed := []struct{ method, path string; body any; status int }{
		{http.MethodGet, "/me", nil, http.StatusOK},
		{http.MethodGet, "/family", nil, http.StatusOK},
		{http.MethodGet, "/children", nil, http.StatusOK},
		{http.MethodGet, "/devices?child_id=" + child.ID, nil, http.StatusOK},
		{http.MethodGet, d + "/desired-state", nil, http.StatusOK},
		{http.MethodPost, c + "/bonus", map[string]any{"minutes": 15}, http.StatusOK},
	}
	for _, a := range allowed {
		if r := h.call(a.method, a.path, guardian.Token, a.body); r.Status != a.status {
			t.Errorf("guardian %s %s: got %d, want %d\n%s", a.method, a.path, r.Status, a.status, r.Body)
		}
	}

	refused := []struct{ method, path string; body any }{
		{http.MethodGet, "/dpc", nil},
		{http.MethodGet, "/family/blocked-packages", nil},
		{http.MethodPut, "/family/blocked-packages", map[string]any{"package_name": "com.example.x"}},
		{http.MethodDelete, "/family/blocked-packages?package_name=com.example.x", nil},
		{http.MethodGet, "/parents", nil},
		{http.MethodPost, "/parents", map[string]any{"email": "x@family.test", "role": "ADMIN"}},
		{http.MethodPatch, "/parents/" + primary.Parent.ID, map[string]any{"role": "ADMIN"}},
		{http.MethodDelete, "/parents/" + primary.Parent.ID, nil},
		{http.MethodPost, "/children", map[string]any{"name": "X"}},
		{http.MethodPatch, c, map[string]any{"name": "X"}},
		{http.MethodDelete, c, nil},
		{http.MethodGet, c + "/policy", nil},
		{http.MethodPatch, c + "/policy", map[string]any{"daily_limit_minutes": 600}},
		{http.MethodGet, c + "/app-rules", nil},
		{http.MethodPut, c + "/app-rules", map[string]any{"package_name": "com.example.x", "action": "ALLOW"}},
		{http.MethodDelete, c + "/app-rules?package_name=com.example.x", nil},
		{http.MethodGet, c + "/blocked-domains", nil},
		{http.MethodPost, c + "/blocked-domains", map[string]any{"domain": "example.com"}},
		{http.MethodDelete, c + "/blocked-domains?domain=example.com", nil},
		{http.MethodPost, c + "/devices", map[string]any{"name": "X"}},
		{http.MethodGet, c + "/managed-apps", nil},
		{http.MethodPut, c + "/managed-apps/com.example.x", nil},
		{http.MethodDelete, c + "/managed-apps/com.example.x", nil},
		{http.MethodGet, d, nil},
		{http.MethodPatch, d, map[string]any{"name": "X"}},
		{http.MethodDelete, d, nil},
		{http.MethodPost, d + "/provisioning", nil},
		{http.MethodGet, d + "/recovery-code", nil},
		{http.MethodGet, d + "/recovery-events", nil},
		{http.MethodGet, d + "/apps", nil},
		{http.MethodDelete, d + "/apps/com.example.x", nil},
		{http.MethodGet, d + "/usage", nil},
		{http.MethodGet, d + "/usage/timeline", nil},
		{http.MethodGet, d + "/locations", nil},
		{http.MethodGet, d + "/commands", nil},
		{http.MethodPost, d + "/commands", map[string]any{"type": "RING"}},
		{http.MethodGet, d + "/debug", nil},
		{http.MethodGet, "/apps", nil},
		{http.MethodPost, "/apps", nil},
		{http.MethodPost, "/apps/scan", nil},
		{http.MethodDelete, "/apps/11111111-2222-3333-4444-555555555555", nil},
		{http.MethodGet, "/api-keys", nil},
		{http.MethodPost, "/api-keys", map[string]any{"name": "x"}},
		{http.MethodPost, "/api-keys/11111111-2222-3333-4444-555555555555/revoke", nil},
		{http.MethodDelete, "/api-keys/11111111-2222-3333-4444-555555555555", nil},
		{http.MethodGet, "/audit", nil},
	}
	for _, r := range refused {
		resp := h.call(r.method, r.path, guardian.Token, r.body)
		if resp.Status != http.StatusForbidden || resp.errorCode() != "forbidden" {
			t.Errorf("guardian %s %s: got %d %q, want 403 forbidden", r.method, r.path, resp.Status, resp.errorCode())
		}
	}
	// Control: the refusals are about the role. An admin reading the same routes is not refused.
	// Reads only, because an admin's writes here would delete the fixture under the rest of the loop.
	for _, r := range refused {
		if r.method != http.MethodGet || r.path == "/api-keys" || r.path == d+"/debug" {
			continue
		}
		if resp := h.call(r.method, r.path, admin.Token, nil); resp.Status == http.StatusForbidden {
			t.Errorf("admin GET %s was refused too, so the guardian's 403 proves nothing about roles", r.path)
		}
	}
	// The phone still exists and the child still has a limit: nothing above landed.
	var pol struct {
		DailyLimitMinutes int `json:"daily_limit_minutes"`
	}
	h.call(http.MethodGet, c+"/policy", primary.Token, nil).expect(http.StatusOK).decode(&pol)
	if pol.DailyLimitMinutes != 60 {
		t.Errorf("a refused write landed: the daily limit is %d", pol.DailyLimitMinutes)
	}
}
```

If a field name in the fixture differs, for example the policy JSON key or the app-rule body, take it from the handler rather than guessing. It must not change the assertion. `/devices/:id/debug` is a streaming upgrade: if a plain GET from an admin hangs, keep it out of the admin control, as the code above does.

- [ ] **Step 2: Run it**

Run: `bash tests/e2e/run.sh -run TestAGuardianIsRefused 2>&1 | tail -20`
Expected: PASS (`rc=0`), because Tasks 1–2 already landed. This test is calibrated in Step 3.

- [ ] **Step 3: Calibrate** (snapshot `server.go` first)

| # | Probe | Must go red | Message |
|---|---|---|---|
| 9 | `GET /children/:id/policy` `admins` → `everyone` | this test | `guardian GET /children/…/policy: got 200` |
| 10 | `POST /children/:id/bonus` `everyone` → `admins` | this test | `guardian POST …/bonus: got 403, want 200` |
| 11 | `GET /audit` `admins` → `primaryOnly` | the admin control | `admin GET /audit was refused too` |

Restore after each probe and `cmp` against the snapshot.

- [ ] **Step 4: Commit** `tests/e2e/roles_test.go`. Run the internals grep first. Message: "e2e: a guardian meets 403 on every route outside the guardian window (FR-20.1)", plus probes 9–11.

---

### Task 4: Console — People & rights, and the guardian view

**Files:**
- Modify: `backend/internal/console/assets/app.js`:
  - `onRoute` (~line 411) and `VIEWS` (~422)
  - `refresh`: the empty-state condition (~455)
  - `renderFamily`: the parents card (~2425–2460)
  - new `loadGuardian` / `renderGuardian`, placed after `bonusButtons` (~2135)
- Modify: `backend/internal/console/assets/app.css` (only if a class is missing; reuse `card`, `btn-grid`, `muted`, `field-row`)
- Create: `tests/e2e/roles_console_test.go`

**Interfaces:**
- Consumes: `PATCH /parents/:id` (Task 2); `POST /parents` with a role; the guardian allowlist (Task 1).
- Produces:
  - `#view .guardian-card`, one per profile.
  - Guardian time buttons, each with `data-minutes`.
  - People & rights `select.role-select`, whose options are `GUARDIAN` / `ADMIN` / `PRIMARY_ADMIN`.

- [ ] **Step 1: Write the failing browser test** — `tests/e2e/roles_console_test.go`:

```go
package e2e

// FR-20 as people meet it in a real browser: the primary admin picks and changes roles, and a
// guardian who signs in sees their window, not an admin console full of refusals.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestThePrimaryAdminPicksAndChangesRolesInTheConsole(t *testing.T) {
	h := newHarness(t)
	h.signIn(primaryParent)
	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	b.navigate(h.base + "/")
	h.issuer.setNextLogin(primaryParent)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.eval("document.querySelector('.tab[data-tab=\"family\"]').click()", nil)
	b.waitFor("!!document.querySelector('#view form.add-person')", 15*time.Second, "the People & rights card")

	b.eval(`(() => { const f = document.querySelector('#view form.add-person');
	  f.querySelector('input[type=email]').value = 'guardian@family.test';
	  f.querySelector('select').value = 'GUARDIAN';
	  f.requestSubmit(); })()`, nil)
	b.waitFor(`Array.from(document.querySelectorAll('#view li')).some((li) => li.textContent.includes('guardian@family.test'))`,
		15*time.Second, "the new person in the list")

	primary := h.signIn(primaryParent)
	roleOf := func(email string) string {
		var list struct {
			Parents []parentDTO `json:"parents"`
		}
		h.call(http.MethodGet, "/parents", primary.Token, nil).expect(http.StatusOK).decode(&list)
		for _, p := range list.Parents {
			if p.Email == email {
				return p.Role
			}
		}
		return ""
	}
	if got := roleOf("guardian@family.test"); got != "GUARDIAN" {
		t.Fatalf("the console added the person as %q, not GUARDIAN", got)
	}

	// Change the role with the row's select. confirm() is answered by the page override below.
	b.eval(`window.confirm = () => true;
	  (() => { const li = Array.from(document.querySelectorAll('#view li')).find((l) => l.textContent.includes('guardian@family.test'));
	    const s = li.querySelector('select.role-select'); s.value = 'ADMIN'; s.dispatchEvent(new Event('change')); })()`, nil)
	deadline := time.Now().Add(10 * time.Second)
	for roleOf("guardian@family.test") != "ADMIN" {
		if time.Now().After(deadline) {
			t.Fatalf("the role select did not change the role; it is %q", roleOf("guardian@family.test"))
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The primary admin's own row offers no select: the server would refuse it.
	var ownSelect bool
	b.eval(`!!Array.from(document.querySelectorAll('#view li')).find((l) => l.textContent.includes('primary@family.test')).querySelector('select')`, &ownSelect)
	if ownSelect {
		t.Error("the primary admin's own row offers a role select")
	}
}

func TestAGuardianSeesTheGuardianViewAndCanGiveTime(t *testing.T) {
	h := newHarness(t)
	primary := h.signIn(primaryParent)
	withPhone := h.newChild(primary.Token, "Mira")
	withoutPhone := h.newChild(primary.Token, "Nils")
	device := h.newDevice(primary.Token, withPhone.ID, "Mira's phone")
	_, enrollToken := h.provision(primary.Token, device.ID)
	h.enrollDevice(enrollToken, "Pixel 8", "Android 16", nil)
	h.call(http.MethodPatch, "/children/"+withPhone.ID+"/policy", primary.Token,
		map[string]any{"daily_limit_minutes": 60, "timezone": "Europe/Zurich"}).expect(http.StatusOK)
	h.addParent(primary.Token, guardianIdentity.Email, "GUARDIAN")

	b := startBrowser(t)
	b.phone(phoneWidth, phoneHeight)
	// Review focus 1: arrive on an old bookmark to an admin page.
	b.navigate(h.base + "/#/rules")
	h.issuer.setNextLogin(guardianIdentity)
	b.waitFor("!document.getElementById('signin').hidden", 15*time.Second, "the sign-in screen")
	b.eval("document.querySelector('#signin a.btn-primary').click()", nil)
	b.waitFor("!document.getElementById('app').hidden", 30*time.Second, "the console to sign in")
	b.waitFor("document.querySelectorAll('#view .guardian-card').length === 2", 15*time.Second,
		"one guardian card per profile")

	var page struct {
		NavHidden bool     `json:"navHidden"`
		Cards     []string `json:"cards"`
		Buttons   []string `json:"buttons"`
	}
	b.eval(`({
	  navHidden: document.getElementById('mainnav').hidden,
	  cards: Array.from(document.querySelectorAll('#view .guardian-card')).map((c) => c.textContent),
	  buttons: Array.from(document.querySelectorAll('#view .guardian-card button[data-minutes]')).map((x) => x.dataset.minutes),
	})`, &page)
	if !page.NavHidden {
		t.Error("a guardian sees the admin tab bar")
	}
	var mira, nils string
	for _, c := range page.Cards {
		if strings.Contains(c, "Mira") {
			mira = c
		}
		if strings.Contains(c, "Nils") {
			nils = c
		}
	}
	if !strings.Contains(mira, "Heute") || !strings.Contains(mira, "60 min") {
		t.Errorf("Mira's card does not show today's time against 60 min: %q", mira)
	}
	// Review focus 4.
	if !strings.Contains(nils, "Noch kein Handy eingerichtet") {
		t.Errorf("a profile with no phone does not say so: %q", nils)
	}
	// Review focus 5: one set of buttons, for the profile that has a limit.
	if fmt.Sprint(page.Buttons) != "[15 30 60]" {
		t.Errorf("time buttons %v, want exactly [15 30 60] (Mira only)", page.Buttons)
	}
	if b.pageErrorReport() != "" {
		t.Errorf("the page reported errors: %s", b.pageErrorReport())
	}

	b.eval(`document.querySelector('#view .guardian-card button[data-minutes="15"]').click()`, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ds struct {
			Desired struct {
				BonusMinutes int `json:"bonus_minutes"`
			} `json:"desired"`
		}
		h.call(http.MethodGet, "/devices/"+device.ID+"/desired-state", primary.Token, nil).
			expect(http.StatusOK).decode(&ds)
		if ds.Desired.BonusMinutes == 15 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("+15 in the guardian view did not reach the server (bonus %d)", ds.Desired.BonusMinutes)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
```

Check that `pageErrorReport` counts a failed fetch (a 403) as a page error. If it does not, also assert no `.toast` text containing "failed" after load. That catches a stray admin request the guardian view still makes, such as `/dpc`.

- [ ] **Step 2: Run to verify it fails**

Run: `bash tests/e2e/run.sh -run 'TestThePrimaryAdminPicks|TestAGuardianSeesTheGuardianView' 2>&1 | tail -20`
Expected: FAIL on `the People & rights card` and `one guardian card per profile` timeouts.

- [ ] **Step 3: Implement in `app.js`**

1. `onRoute`: after computing `state.view`, add
   ```js
   // FR-20: a guardian has one page. An old link to an admin page lands there too, rather than on a
   // "Could not load this page" made of 403s.
   if (isGuardian()) state.view = 'guardian';
   ```
   Add the helper `function isGuardian() { return !!state.parent && state.parent.role === 'GUARDIAN'; }`.
2. `VIEWS`: add `guardian: { load: loadGuardian, render: renderGuardian },`.
3. `boot`, after `state.dpc = dpc;`:
   ```js
   if (isGuardian()) {
     document.getElementById('mainnav').hidden = true;
     document.getElementById('child-switcher').hidden = true;
     state.dpc = null;
   }
   ```
   `/dpc` answers a guardian 403, and `boot` already catches that into `null`. Leave the catch in place.
4. `refresh`: change `if (!state.childId && state.view !== 'family')` to `if (!state.childId && state.view !== 'family' && state.view !== 'guardian')`.
5. After `bonusButtons`:
   ```js
   /* ---- guardian ----------------------------------------------------------- */

   /* The guardian window (FR-20, spec §6). One card per profile, in German, because the people this
      page is for are the family's non-admins. Phase 2 adds pause and −time; phase 3 adds the tasks
      waiting for confirmation above the cards. */
   async function loadGuardian() {
     return Promise.all(state.children.map(async (child) => {
       const devices = ((await api('/devices?child_id=' + encodeURIComponent(child.id))).devices || [])
         .filter((d) => d.enrolled);
       const states = await Promise.all(devices.map((d) =>
         api('/devices/' + d.id + '/desired-state').then((r) => (r && r.desired) || null).catch(() => null)));
       return { child, devices: devices.map((dev, i) => ({ dev, desired: states[i] })) };
     }));
   }

   function renderGuardian(profiles) {
     if (!profiles.length) {
       return [el('div', { class: 'card full' }, el('h2', { text: 'Noch kein Profil' }),
         el('p', { class: 'muted', text: 'Ein Admin richtet die Profile ein.' }))];
     }
     return profiles.map(({ child, devices }) => {
       const card = el('div', { class: 'card full guardian-card' }, el('h2', { text: child.name }));
       if (!devices.length) {
         card.append(el('p', { class: 'muted', text: 'Noch kein Handy eingerichtet.' }));
         return card;
       }
       let hasLimit = false;
       for (const { dev, desired } of devices) {
         if (!desired) {
           card.append(el('p', { class: 'muted', text: dev.name + ': noch keine Angaben vom Handy.' }));
           continue;
         }
         const used = desired.used_minutes || 0;
         const quota = desired.quota_minutes || 0;
         hasLimit = hasLimit || quota > 0;
         card.append(el('p', { text: dev.name + ' — Heute ' + fmtMinutes(used)
           + (quota > 0 ? ' von ' + fmtMinutes(quota) : ' (kein Tageslimit)')
           + (desired.bonus_minutes ? ' (inkl. ' + fmtMinutes(desired.bonus_minutes) + ' extra)' : '') }));
         if (desired.suspend_reason) {
           card.append(el('p', { class: 'muted', text: 'Apps pausiert: ' + guardianReason(desired.suspend_reason) + '.' }));
         }
       }
       if (hasLimit) card.append(guardianTimeButtons(child));
       return card;
     });
   }

   function guardianReason(reason) {
     return ({ QUOTA: 'Tageslimit erreicht', BEDTIME: 'Bettzeit' })[reason] || reason.toLowerCase();
   }

   function guardianTimeButtons(child) {
     const grant = (minutes) => el('button', {
       class: 'btn', type: 'button', text: '+' + minutes + ' min', 'data-minutes': String(minutes),
       'aria-label': minutes + ' Minuten mehr für heute',
       onclick: () => act('+' + minutes + ' min für heute', async () => {
         await api('/children/' + child.id + '/bonus', { method: 'POST', body: { minutes } });
         refresh();
       }),
     });
     return el('div', { class: 'stack' },
       el('span', { class: 'muted', text: 'Mehr Zeit, nur heute:' }),
       el('div', { class: 'btn-grid' }, grant(15), grant(30), grant(60)));
   }
   ```
   Check the real `suspend_reason` values with `grep -n "ReasonQuota\|ReasonBedtime\|Reason[A-Z][a-z]* *=" backend/internal/policy/engine.go` and use the exact strings in `guardianReason`.
6. `renderFamily`, the parents card:
   - Title `People & rights`, with one muted line: *"Primary admin: everything. Admin: everything except people and API keys. Guardian: the guardian window only — see today, give time."*
   - For the primary admin, every row except their own gets `el('select', { class: 'role-select', 'aria-label': 'Role of ' + p.email, onchange })`. Its options are GUARDIAN "Guardian", ADMIN "Admin" and PRIMARY_ADMIN "Primary admin", with the current role `selected`. Set `selected` on the option; `[value]` on a select whose options are added afterwards does not take.
   - `onchange`: `if (!confirm('Make ' + p.email + ' ' + label + '?')) { e.target.value = p.role; return; }`, then `await act('Role changed', () => api('/parents/' + p.id, { method: 'PATCH', body: { role: e.target.value } })); refresh();`.
   - The add form gets `class: 'field-row add-person'`, plus a role `select` before the button, defaulting to `GUARDIAN`. Submit sends `{ email, role: select.value }`.
   - The Remove button stays as it is.

- [ ] **Step 4: Run to verify it passes**

Run: `bash tests/e2e/run.sh -run 'TestThePrimaryAdminPicks|TestAGuardianSeesTheGuardianView' 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Look at it.** Screenshot the guardian view and the People & rights card at 360×740 in the e2e browser, or with the Playwright MCP against a local server, and read the image. A green test does not show layout: check that nothing overflows, the buttons are ≥ 44 px, and dark mode works.

- [ ] **Step 6: Calibrate** (snapshot `app.js` first)

| # | Probe | Must go red | Message |
|---|---|---|---|
| 12 | `if (isGuardian()) state.view = 'guardian'` → `if (false) …` | TestAGuardianSeesTheGuardianView | `one guardian card per profile` timeout |
| 13 | `if (hasLimit) card.append(…)` → `card.append(…)` unconditionally | same | `time buttons [15 30 60 15 30 60]` |
| 14 | add form sends `role: 'ADMIN'` instead of `select.value` | TestThePrimaryAdminPicks… | `added the person as "ADMIN"` |
| 15 | the row select is shown for `p.id === state.parent.id` too | same | `own row offers a role select` |

Restore after each probe and `cmp` against the snapshot.

- [ ] **Step 7: Full e2e suite** — `bash tests/e2e/run.sh 2>&1 | tail -5`, expect `rc=0`. The console layout tests (`mobile_test.go`, `laptop_test.go`) measure the Family tab. A red there is either a real overflow from the new select (fix the CSS) or a text assertion on "Parents" (update it to "People & rights").

- [ ] **Step 8: Commit** `app.js`, `app.css` if changed, `tests/e2e/roles_console_test.go` and any updated layout test. Run the internals grep first. Message: "console: People & rights, and a guardian sees the guardian window (FR-20.3)", plus probes 12–15.

---

### Task 5: Requirements and implementation plan

**Files:**
- Modify: `REQUIREMENTS.md` — §2 Actors (the RBAC paragraph) and a new `### FR-20 Roles and rights` after FR-19
- Modify: `IMPLEMENTATION_PLAN.md` — new `## Phase 34 — Roles and rights (FR-20)` after Phase 33

- [ ] **Step 1: FR-20 already exists** (entered 2026-09-27 with the spec, marked *not built yet*, so the citation guard stays green). Read `### FR-20` in `REQUIREMENTS.md`.

- [ ] **Step 2: Bring FR-20 up to what was built.** Make FR-20.1–20.3 match the code exactly: the guardian allowlist, the 409 on self-change, the concurrency rule and the German guardian view. Remove phase 1 from the "none of them is built yet" sentence above FR-20. Replace the Actors paragraph (*"RBAC roles: … `PRIMARY_ADMIN` is the only role that may add or remove parents; the others differ only in that."*), which is no longer true, with a pointer to FR-20.

- [ ] **Step 3: Write Phase 34** in the Phase 33 format:
  - the owner's words and the finding (7 of 48 routes checked the role);
  - 34.1, the tests;
  - 34.2, the calibration table, probes 1–15, each with its observed result, including any that stayed green and why;
  - 34.3, live verification, filled in during Task 6.

- [ ] **Step 4: Run the doc-scanning tests with `--rerun-tasks`.** Gradle replays a stale green for them otherwise. Run `cd android-dpc && ./gradlew testDebugUnitTest --rerun-tasks 2>&1 | tail -5; echo "rc=${PIPESTATUS[0]}"`. Also run the Go tests that read the docs: `cd backend && go test ./... 2>&1 | tail -5`.

- [ ] **Step 5: Commit** both docs, after the internals grep.

---

### Task 6: Release, deploy, verify live

**Files:**
- Modify: `android-dpc/app/build.gradle.kts` (versionCode `27 + buildOffset`, versionName `"0.6.18"`)
- Modify: `deploy/control-plane.yaml` (image tag `0.6.18`)
- Modify (argocd repo, private): `apps/familyguard/control-plane/control-plane.yaml`

- [ ] **Step 1: Bump, commit, tag.** Bump both files, then commit ("release 0.6.18"). Then `git tag v0.6.18 && git push origin main v0.6.18`. Read both back with `git ls-remote origin refs/heads/main refs/tags/v0.6.18`.
- [ ] **Step 2: Wait for the Release workflow to go green.** Read the GHCR index digest for `0.6.18` anonymously.
- [ ] **Step 3: Build the APK.** Run `assembleRelease`, then zipalign, then `apksigner` with `--ks-pass env:KSPASS`. Verify the certificate digest is `b62cda94…8e10`. Stage the file as `familyguard-0.6.18-versionCode-27.apk` next to the current one, then swap `familyguard.apk` atomically. Record the sha256.
- [ ] **Step 4: Deploy through argocd.** One commit sets the image `0.6.18` and the `apk-sha256` annotation. Push it to `main` and read it back with `ls-remote`.
- [ ] **Step 5: Wait for the rollout.** Wait for exactly one control-plane pod on the new digest, then check `/readyz` = 200 and that `/dpc.apk`'s sha matches.
- [ ] **Step 6: Verify live, read-only.**
  - `GET /api/v1/parents` with the owner's key (from `~/.config/familyguard/config.json`, never printed) lists the roles.
  - `curl -s https://<host>/app.js | grep -c "People & rights"` must be ≥ 1, and a string that cannot be in the file must count 0.
  - `PATCH /parents/:id` with the key must answer 403 `api_key_forbidden`. That proves the route is live and console-only without changing anything.
  - The live DB still has no GUARDIAN (checked at planning: one PRIMARY_ADMIN only), so nobody lost access.
- [ ] **Step 7: Update the phone.** Run `fgctl update <device>` and wait for "(27)". The phone is reachable only while awake; if it sleeps, record that the update is pending rather than blocking the phase on it.
- [ ] **Step 8: Close the phase.**
  - Fill in Phase 34.3 and commit it.
  - Update the memory file `project_familyguard_open_items.md`.
  - Tell the owner the guardian account can now be added: Family → People & rights → email → Guardian.

---

## What follows (separate plans, written when each phase starts)

- **Phase 2 — Pause and today's time.**
  - `policies.paused` and the family communication list, in both engines with shared vectors.
  - A signed day adjustment replaces `bonus_minutes > 0`.
  - The guardian window gains *Sperren/Entsperren* and −15, and admins get it as their first tab.
- **Phase 3 — Daily plan and earned time.**
  - Plan tables, task reporting from the phone, confirmation, credits and the 7-day ledger.
  - The `BONUS` app rule and the engine precedence of spec §5.
  - The Today screen on the phone.
- **Phase 4 — Alarm.** Measure `setAlarmClock` and the full-screen intent on the family phone first.
- **Phase 5 — Agenda and holidays.**
- **Phase 6 — Optional:** calendar import.
