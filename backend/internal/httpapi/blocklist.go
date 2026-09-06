package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ---- the family-wide blocklist (FR-18) ------------------------------------
//
// One list, every child, including the ones added after it was written. The per-child app rules in
// parents.go are unchanged and still the finer instrument; this is for the decision that was never
// about a particular child — a vendor preinstall nobody in the household asked for.
//
// Writes are admin-only. A per-child rule affects one phone and a guardian can reasonably set one;
// an entry here changes every phone in the family at once, which is a different kind of decision.

// maxBlocklistText bounds the two free-text columns. Neither is interpreted — the console prints
// them — but an unbounded string in a row every device read fans out through is a row worth
// bounding. The reason is meant to be a sentence.
const maxBlocklistText = 200

func (s *Server) listFamilyBlocklist(c *gin.Context) {
	entries, err := s.store.ListFamilyBlockedPackages(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"packages": entries})
}

type blocklistRequest struct {
	PackageName string `json:"package_name"`
	Label       string `json:"label"`
	Reason      string `json:"reason"`
}

func (s *Server) putFamilyBlocklist(c *gin.Context) {
	var req blocklistRequest
	if !bindJSON(c, &req) {
		return
	}
	pkg := strings.TrimSpace(req.PackageName)
	// Shape-checked rather than merely non-empty. A package name is the only field here that is
	// acted on, and the console offers it from an inventory the phone reported — so anything that
	// is not shaped like a package name arrived by hand or by mistake, and blocking a string that
	// can never match an installed app is a row that looks like protection and is not.
	if !validPackageName(pkg) {
		failWith(c, http.StatusBadRequest, "invalid_input",
			"package_name must be an Android package name, for example com.example.app")
		return
	}
	label := trimTo(req.Label, maxBlocklistText)
	reason := trimTo(req.Reason, maxBlocklistText)

	fam, err := s.store.GetFamily(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	entry, err := s.store.SetFamilyBlockedPackage(c.Request.Context(), fam.ID, pkg, label, reason)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.blocklistChanged(c, "FAMILY_BLOCKLIST_SET", map[string]any{"package": pkg})
	c.JSON(http.StatusOK, entry)
}

func (s *Server) deleteFamilyBlocklist(c *gin.Context) {
	pkg := strings.TrimSpace(c.Query("package_name"))
	if pkg == "" {
		failWith(c, http.StatusBadRequest, "invalid_input", "package_name is required")
		return
	}
	fam, err := s.store.GetFamily(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.store.DeleteFamilyBlockedPackage(c.Request.Context(), fam.ID, pkg); err != nil {
		s.fail(c, err)
		return
	}
	s.blocklistChanged(c, "FAMILY_BLOCKLIST_CLEARED", map[string]any{"package": pkg})
	c.Status(http.StatusNoContent)
}

// blocklistChanged records the change and tells every enrolled device to re-read its policy.
//
// The store already bumped every child's policy version inside the same transaction as the write,
// so a device that reconnects on its own will pick the change up regardless of what happens here.
// This is the fast path, and it fans out over children rather than devices because that is the
// shape notifyChild already has — one list, walked once, so a family with three phones does not
// depend on three separate pushes all succeeding.
func (s *Server) blocklistChanged(c *gin.Context, action string, detail map[string]any) {
	s.auditParent(c, action, "family", "", detail)
	children, err := s.store.ListChildren(c.Request.Context())
	if err != nil {
		// The write landed and the versions were bumped; failing here means devices notice at their
		// next poll instead of immediately. Logged rather than returned as a 500, which would
		// invite a retry that changes nothing.
		s.log.Error("could not fan out a blocklist change", "error", err, "request_id", RequestIDOf(c))
		return
	}
	for _, ch := range children {
		s.notifyChild(c, ch.ID, "policy")
	}
}

// trimTo trims surrounding space and cuts to at most n bytes, on a rune boundary.
//
// Cutting on a byte boundary would be the easy version and would produce invalid UTF-8 in a JSON
// document — a label a parent typed in German or with an emoji is exactly the input that finds it.
func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
