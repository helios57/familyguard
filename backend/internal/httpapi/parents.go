package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/store"
)

func (s *Server) getFamily(c *gin.Context) {
	fam, err := s.store.GetFamily(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, fam)
}

// ---- parents --------------------------------------------------------------

func (s *Server) listParents(c *gin.Context) {
	parents, err := s.store.ListParents(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"parents": parents})
}

type createParentRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (s *Server) createParent(c *gin.Context) {
	var req createParentRequest
	if !bindJSON(c, &req) {
		return
	}
	email := store.NormalizeEmail(req.Email)
	if !strings.Contains(email, "@") || strings.HasPrefix(email, "@") {
		failWith(c, http.StatusBadRequest, "invalid_input", "that is not an email address")
		return
	}
	switch req.Role {
	case store.RolePrimaryAdmin, store.RoleAdmin, store.RoleGuardian:
	default:
		failWith(c, http.StatusBadRequest, "invalid_input", "role must be PRIMARY_ADMIN, ADMIN or GUARDIAN")
		return
	}

	fam, err := s.store.GetFamily(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	parent, err := s.store.CreateParent(c.Request.Context(), fam.ID, email, req.Role)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "PARENT_ADDED", "parent", parent.ID.String(), map[string]any{
		"email": parent.Email, "role": parent.Role,
	})
	c.JSON(http.StatusCreated, parent)
}

func (s *Server) deleteParent(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	// Removing yourself is refused here rather than in the store, because the store's rule is
	// about the family (never zero primary admins) and this one is about the request (a parent
	// who deletes their own account is locked out with no way back in).
	if p := parentOf(c); p != nil && p.ID == id {
		failWith(c, http.StatusConflict, "conflict", "you cannot remove your own account")
		return
	}
	if err := s.store.DeleteParent(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "PARENT_REMOVED", "parent", id.String(), nil)
	c.Status(http.StatusNoContent)
}

// ---- children -------------------------------------------------------------

func (s *Server) listChildren(c *gin.Context) {
	children, err := s.store.ListChildren(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"children": children})
}

type childRequest struct {
	Name      string `json:"name"`
	BirthYear *int   `json:"birth_year"`
}

func (s *Server) createChild(c *gin.Context) {
	var req childRequest
	if !bindJSON(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "a child needs a name")
		return
	}
	fam, err := s.store.GetFamily(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	child, err := s.store.CreateChild(c.Request.Context(), fam.ID, name, req.BirthYear)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "CHILD_ADDED", "child", child.ID.String(), map[string]any{"name": child.Name})
	c.JSON(http.StatusCreated, child)
}

func (s *Server) updateChild(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req childRequest
	if !bindJSON(c, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "a child needs a name")
		return
	}
	child, err := s.store.UpdateChild(c.Request.Context(), id, name, req.BirthYear)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "CHILD_UPDATED", "child", id.String(), map[string]any{"name": child.Name})
	c.JSON(http.StatusOK, child)
}

func (s *Server) deleteChild(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := s.store.DeleteChild(c.Request.Context(), id); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "CHILD_REMOVED", "child", id.String(), nil)
	c.Status(http.StatusNoContent)
}

// ---- policy ---------------------------------------------------------------

func (s *Server) getPolicy(c *gin.Context) {
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	pol, err := s.store.GetPolicy(c.Request.Context(), id)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, pol)
}

// patchPolicyRequest mirrors store.PolicyUpdate: every field is a pointer, so omitting one leaves
// it alone. A console that sent the whole object back would otherwise revert a change another
// parent made between the read and the write.
type patchPolicyRequest struct {
	TrackingOnly       *bool   `json:"tracking_only"`
	AllowChildInstalls *bool   `json:"allow_child_installs"`
	YouTubeBlocked     *bool   `json:"youtube_blocked"`
	DailyLimitMinutes  *int    `json:"daily_limit_minutes"`
	BedtimeEnabled     *bool   `json:"bedtime_enabled"`
	BedtimeStart       *string `json:"bedtime_start"`
	BedtimeEnd         *string `json:"bedtime_end"`
	DNSHost            *string `json:"dns_host"`
	Timezone           *string `json:"timezone"`
}

func (s *Server) patchPolicy(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req patchPolicyRequest
	if !bindJSON(c, &req) {
		return
	}

	// Validate before writing. Every one of these is rejected by the database too, but a CHECK
	// violation reaches the parent as "something went wrong" — the same refusal, minus the reason.
	var bad []string
	if req.DailyLimitMinutes != nil && (*req.DailyLimitMinutes < 0 || *req.DailyLimitMinutes > 1440) {
		bad = append(bad, "daily limit must be between 0 and 1440 minutes")
	}
	for _, f := range []struct {
		name string
		val  *string
	}{{"bedtime_start", req.BedtimeStart}, {"bedtime_end", req.BedtimeEnd}} {
		if f.val != nil && !validHHMM(*f.val) {
			bad = append(bad, f.name+" must look like 21:00")
		}
	}
	if req.Timezone != nil {
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			bad = append(bad, "unknown time zone "+*req.Timezone)
		}
	}
	if req.DNSHost != nil && strings.TrimSpace(*req.DNSHost) == "" {
		// An empty DNS host would disable filtering while the console still showed it as on.
		bad = append(bad, "dns_host cannot be empty — set the family default rather than clearing it")
	}
	if len(bad) > 0 {
		// No problem string may contain "; ": it is the separator, and a caller splitting the
		// message back into a list would silently read one problem as two. The em dash above is
		// there for that reason and not for typography.
		failWith(c, http.StatusBadRequest, "invalid_input", strings.Join(bad, "; "))
		return
	}

	pol, err := s.store.UpdatePolicy(c.Request.Context(), childID, store.PolicyUpdate{
		TrackingOnly:       req.TrackingOnly,
		AllowChildInstalls: req.AllowChildInstalls,
		YouTubeBlocked:     req.YouTubeBlocked,
		DailyLimitMinutes:  req.DailyLimitMinutes,
		BedtimeEnabled:     req.BedtimeEnabled,
		BedtimeStart:       req.BedtimeStart,
		BedtimeEnd:         req.BedtimeEnd,
		DNSHost:            req.DNSHost,
		Timezone:           req.Timezone,
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "POLICY_UPDATED", "child", childID.String(), map[string]any{"version": pol.Version})
	s.notifyChild(c, childID, "policy")
	c.JSON(http.StatusOK, pol)
}

// validHHMM accepts exactly the 24-hour form the schema constrains. It is duplicated from the
// CHECK constraint on purpose: the constraint is the authority, and this is the message.
func validHHMM(v string) bool {
	if len(v) != 5 || v[2] != ':' {
		return false
	}
	for i, r := range v {
		if i == 2 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	h := int(v[0]-'0')*10 + int(v[1]-'0')
	m := int(v[3]-'0')*10 + int(v[4]-'0')
	return h < 24 && m < 60
}

// ---- app rules ------------------------------------------------------------

func (s *Server) listAppRules(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	rules, err := s.store.ListAppRules(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

type appRuleRequest struct {
	PackageName string `json:"package_name"`
	Action      string `json:"action"`
}

func (s *Server) putAppRule(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req appRuleRequest
	if !bindJSON(c, &req) {
		return
	}
	pkg := strings.TrimSpace(req.PackageName)
	if pkg == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "package_name is required")
		return
	}
	if req.Action != store.ActionAllow && req.Action != store.ActionBlock {
		failWith(c, http.StatusBadRequest, "invalid_input", "action must be ALLOW or BLOCK")
		return
	}
	if err := s.store.SetAppRule(c.Request.Context(), childID, pkg, req.Action); err != nil {
		s.fail(c, err)
		return
	}
	s.bumpAndNotify(c, childID, "APP_RULE_SET", map[string]any{"package": pkg, "action": req.Action})
	c.JSON(http.StatusOK, gin.H{"package_name": pkg, "action": req.Action})
}

func (s *Server) deleteAppRule(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	pkg := strings.TrimSpace(c.Query("package_name"))
	if pkg == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "package_name is required")
		return
	}
	if err := s.store.DeleteAppRule(c.Request.Context(), childID, pkg); err != nil {
		s.fail(c, err)
		return
	}
	s.bumpAndNotify(c, childID, "APP_RULE_CLEARED", map[string]any{"package": pkg})
	c.Status(http.StatusNoContent)
}

// ---- blocked domains ------------------------------------------------------

func (s *Server) listBlockedDomains(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	domains, err := s.store.ListBlockedDomains(c.Request.Context(), childID)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"domains": domains})
}

type domainRequest struct {
	Domain string `json:"domain"`
}

func (s *Server) addBlockedDomain(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var req domainRequest
	if !bindJSON(c, &req) {
		return
	}
	domain := store.NormalizeDomain(req.Domain)
	if domain == "" || !strings.Contains(domain, ".") {
		failWith(c, http.StatusBadRequest, "invalid_input", "that is not a domain")
		return
	}
	if err := s.store.AddBlockedDomain(c.Request.Context(), childID, domain); err != nil {
		s.fail(c, err)
		return
	}
	s.bumpAndNotify(c, childID, "DOMAIN_BLOCKED", map[string]any{"domain": domain})
	c.JSON(http.StatusCreated, gin.H{"domain": domain})
}

func (s *Server) removeBlockedDomain(c *gin.Context) {
	childID, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	domain := store.NormalizeDomain(c.Query("domain"))
	if domain == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "domain is required")
		return
	}
	if err := s.store.RemoveBlockedDomain(c.Request.Context(), childID, domain); err != nil {
		s.fail(c, err)
		return
	}
	s.bumpAndNotify(c, childID, "DOMAIN_UNBLOCKED", map[string]any{"domain": domain})
	c.Status(http.StatusNoContent)
}

// bumpAndNotify raises the policy version and tells the child's devices there is new state.
//
// The version bump is what makes a rule change visible to a device: the policy row itself did not
// change, so without it a device comparing versions would never re-fetch, and the parent's change
// would apply only after some unrelated edit.
func (s *Server) bumpAndNotify(c *gin.Context, childID uuid.UUID, action string, detail map[string]any) {
	version, err := s.store.BumpPolicyVersion(c.Request.Context(), childID)
	if err != nil {
		// The rule itself is written; failing to bump means devices apply it late rather than
		// never, so this is logged rather than turned into a 500 the parent would retry.
		s.log.Error("policy version bump failed", "child", childID, "error", err,
			"request_id", RequestIDOf(c))
	} else if detail != nil {
		detail["version"] = version
	}
	s.auditParent(c, action, "child", childID.String(), detail)
	s.notifyChild(c, childID, "policy")
}

// ---- audit ----------------------------------------------------------------

func (s *Server) listAudit(c *gin.Context) {
	limit := queryInt(c, "limit", 100, 1, 500)
	entries, err := s.store.ListAudit(c.Request.Context(), limit)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// notifyChild pushes an event to every enrolled device of a child, and to the console.
func (s *Server) notifyChild(c *gin.Context, childID uuid.UUID, kind string) {
	ids, err := s.store.DeviceIDsForChild(c.Request.Context(), childID)
	if err != nil {
		s.log.Error("could not fan out policy change", "child", childID, "error", err)
		return
	}
	for _, id := range ids {
		s.hub.PublishDevice(id, Event{Type: kind})
	}
	s.hub.PublishParents(Event{Type: kind, ChildID: childID.String()})
}
