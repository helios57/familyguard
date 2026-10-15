package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/helios57/familyguard/backend/internal/catalog"
	"github.com/helios57/familyguard/backend/internal/store"
)

// ---- what every child is declared to have (FR-16.7) --------------------------------------------

func (s *Server) listFamilyManagedApps(c *gin.Context) {
	packages, err := s.store.FamilyManagedPackages(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	views, ok := s.managedAppViews(c, packages)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"managed_apps": views})
}

// declareFamilyManagedApp adds a package to every child's set, including a child added later.
func (s *Server) declareFamilyManagedApp(c *gin.Context) {
	pkg, ok := packageParam(c)
	if !ok {
		return
	}
	if !s.declarable(c, pkg) {
		return
	}
	family, err := s.store.GetFamily(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.store.DeclareFamilyManagedApp(c.Request.Context(), family.ID, pkg); err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "FAMILY_APP_DECLARED", "family", "", map[string]any{"package_name": pkg})
	s.tellEveryChild(c)
	c.Status(http.StatusNoContent)
}

// withdrawFamilyManagedApp removes a package from the family's set. A child that also declares it
// on its own keeps it: that is a separate decision, made about that child.
func (s *Server) withdrawFamilyManagedApp(c *gin.Context) {
	pkg, ok := packageParam(c)
	if !ok {
		return
	}
	family, err := s.store.GetFamily(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	removed, err := s.store.WithdrawFamilyManagedApp(c.Request.Context(), family.ID, pkg)
	if err != nil {
		s.fail(c, err)
		return
	}
	if !removed {
		failWith(c, http.StatusNotFound, "not_found", pkg+" is not declared for every child")
		return
	}
	s.auditParent(c, "FAMILY_APP_WITHDRAWN", "family", "", map[string]any{"package_name": pkg})
	s.tellEveryChild(c)
	c.Status(http.StatusNoContent)
}

// tellEveryChild tells every child's phones that their policy changed. The policy versions were
// bumped in the write's own transaction; this is only the fast path. The audit row is written by the
// caller, with its action as a literal, so tests/e2e/audit_test.go can see it.
func (s *Server) tellEveryChild(c *gin.Context) {
	children, err := s.store.ListChildren(c.Request.Context())
	if err != nil {
		s.log.Error("could not fan out a family app change", "error", err, "request_id", RequestIDOf(c))
		return
	}
	for _, ch := range children {
		s.notifyChild(c, ch.ID, "policy")
	}
}

// declarable reports whether a package may be declared: the catalog carries a version of it, or the
// server follows it on Google Play and will fetch one (FR-16.8). The second case is what lets a
// parent declare a game before Google has finished reviewing it; until a build exists the
// declaration reads as unavailable, which is the honest state.
func (s *Server) declarable(c *gin.Context, pkg string) bool {
	_, err := s.store.LatestApp(c.Request.Context(), pkg)
	if err == nil {
		return true
	}
	if !errors.Is(err, store.ErrNotFound) {
		s.fail(c, err)
		return false
	}
	if _, err := s.store.GetPlaySource(c.Request.Context(), pkg); err == nil {
		return true
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(c, err)
		return false
	}
	failWith(c, http.StatusBadRequest, "unknown_package",
		"no version of "+pkg+" is registered and it is not followed on Google Play; upload it or follow it first")
	return false
}

// ---- Google Play as a source of builds (FR-16.8) -----------------------------------------------

// trackPattern is what Play calls a track: production, beta, alpha, internal, or a custom closed
// testing track, whose names are short and plain.
var trackPattern = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,64}$`)

const maxPlayTracks = 8

type playSourcesView struct {
	Configured bool               `json:"configured"`
	Account    string             `json:"account,omitempty"`
	Interval   string             `json:"interval,omitempty"`
	Sources    []store.PlaySource `json:"sources"`
}

func (s *Server) listPlaySources(c *gin.Context) {
	sources, err := s.store.ListPlaySources(c.Request.Context())
	if err != nil {
		s.fail(c, err)
		return
	}
	view := playSourcesView{Configured: s.play != nil, Sources: sources}
	if s.play != nil {
		view.Account = s.play.Account()
		view.Interval = s.cfg.PlaySyncInterval.String()
	}
	c.JSON(http.StatusOK, view)
}

// putPlaySource follows a package on Google Play. It is accepted whether or not Play access is
// configured yet: following is the parent's decision, access is the operator's, and the listing says
// which one is missing.
func (s *Server) putPlaySource(c *gin.Context) {
	pkg, ok := packageParam(c)
	if !ok {
		return
	}
	if !validPackageName(pkg) {
		failWith(c, http.StatusBadRequest, "invalid_input", "that is not a package name")
		return
	}
	if pkg == s.cfg.DPCPackage() {
		failWith(c, http.StatusBadRequest, "reserved_package",
			pkg+" is the FamilyGuard app itself, which updates through /device/apk-info")
		return
	}
	var body struct {
		Tracks []string `json:"tracks"`
		Label  string   `json:"label"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		failWith(c, http.StatusBadRequest, "invalid_input", "the body must be JSON: {\"tracks\": [...], \"label\": \"...\"}")
		return
	}
	if len(body.Tracks) == 0 {
		body.Tracks = []string{"production"}
	}
	if len(body.Tracks) > maxPlayTracks {
		failWith(c, http.StatusBadRequest, "invalid_input", fmt.Sprintf("at most %d tracks", maxPlayTracks))
		return
	}
	seen := map[string]bool{}
	tracks := make([]string, 0, len(body.Tracks))
	for _, t := range body.Tracks {
		if !trackPattern.MatchString(t) {
			failWith(c, http.StatusBadRequest, "invalid_input", fmt.Sprintf("%q is not a Play track name", t))
			return
		}
		if !seen[t] {
			seen[t] = true
			tracks = append(tracks, t)
		}
	}
	src, err := s.store.PutPlaySource(c.Request.Context(), pkg, tracks, trimTo(body.Label, 100))
	if err != nil {
		s.fail(c, err)
		return
	}
	s.auditParent(c, "PLAY_SOURCE_SET", "app", "", map[string]any{"package_name": pkg, "tracks": tracks})
	// Look now rather than at the next tick: a parent who has just followed an app expects to see
	// whether the server can read it.
	if s.play != nil {
		go s.SyncPlay(context.WithoutCancel(c.Request.Context()), pkg)
	}
	c.JSON(http.StatusOK, src)
}

func (s *Server) deletePlaySource(c *gin.Context) {
	pkg, ok := packageParam(c)
	if !ok {
		return
	}
	removed, err := s.store.DeletePlaySource(c.Request.Context(), pkg)
	if err != nil {
		s.fail(c, err)
		return
	}
	if !removed {
		failWith(c, http.StatusNotFound, "not_found", pkg+" is not followed on Google Play")
		return
	}
	s.auditParent(c, "PLAY_SOURCE_REMOVED", "app", "", map[string]any{"package_name": pkg})
	c.Status(http.StatusNoContent)
}

// syncPlaySources runs one check of every followed package now and answers with what it did.
func (s *Server) syncPlaySources(c *gin.Context) {
	if s.play == nil {
		failWith(c, http.StatusNotFound, "not_configured",
			"this server has no Google Play access (PLAY_SERVICE_ACCOUNT is unset)")
		return
	}
	res := s.SyncPlay(c.Request.Context())
	s.auditParent(c, "PLAY_SYNCED", "app", "", map[string]any{
		"registered": len(res.Registered), "errors": len(res.Errors),
	})
	c.JSON(http.StatusOK, res)
}

// PlaySyncResult is what one check found.
type PlaySyncResult struct {
	Checked    int               `json:"checked"`
	Registered []store.App       `json:"registered"`
	Errors     map[string]string `json:"errors"`
}

// SyncPlay checks the followed packages — all of them, or only those named — and fetches each one's
// newest published build that the catalog does not have yet.
//
// A new build is announced to every child that has the package, directly or through the family
// set: their policy version is bumped and their phones are told, so the update lands within
// minutes of Google publishing it rather than at the phone's next unrelated sync. The phone lifts
// no_install_apps only for the install itself (FR-16.3), which is what lets a child's phone take
// the update while the child still cannot install anything.
//
// Runs are serialised: the loop and a parent's "check now" would otherwise download the same file
// twice and race to register it.
func (s *Server) SyncPlay(ctx context.Context, only ...string) PlaySyncResult {
	res := PlaySyncResult{Registered: []store.App{}, Errors: map[string]string{}}
	if s.play == nil {
		return res
	}
	s.playMu.Lock()
	defer s.playMu.Unlock()

	sources, err := s.store.ListPlaySources(ctx)
	if err != nil {
		res.Errors["*"] = err.Error()
		return res
	}
	want := map[string]bool{}
	for _, p := range only {
		want[p] = true
	}
	for _, src := range sources {
		if len(want) > 0 && !want[src.PackageName] {
			continue
		}
		res.Checked++
		app, published, err := s.syncPlaySource(ctx, src)
		msg := ""
		if err != nil {
			msg = err.Error()
			res.Errors[src.PackageName] = msg
			s.log.Warn("play: check failed", "package", src.PackageName, "error", err)
		}
		if recErr := s.store.RecordPlayCheck(ctx, src.PackageName, s.now(), published, msg); recErr != nil {
			s.log.Error("play: could not record a check", "package", src.PackageName, "error", recErr)
		}
		if app != nil {
			res.Registered = append(res.Registered, *app)
			s.log.Info("play: fetched a new published build", "package", app.PackageName,
				"version_code", app.VersionCode, "sha256", app.SHA256)
			s.announceNewBuild(ctx, app)
		}
	}
	return res
}

// syncPlaySource returns the build it registered (nil when there was nothing new) and the newest
// published version code Play reported (nil when nothing is published yet).
func (s *Server) syncPlaySource(ctx context.Context, src store.PlaySource) (*store.App, *int64, error) {
	rel, err := s.play.LatestPublished(ctx, src.PackageName, src.Tracks)
	if err != nil {
		return nil, nil, err
	}
	if rel == nil {
		return nil, nil, nil
	}
	published := rel.VersionCode
	if _, err := s.store.AppVersion(ctx, src.PackageName, rel.VersionCode); err == nil {
		return nil, &published, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, &published, err
	}
	// Never downgrade: a newer build in the catalog (an upload, say) stays what the phones get.
	if latest, err := s.store.LatestApp(ctx, src.PackageName); err == nil && latest.VersionCode > rel.VersionCode {
		return nil, &published, nil
	}
	uni, err := s.play.Universal(ctx, src.PackageName, rel.VersionCode)
	if err != nil {
		return nil, &published, err
	}
	dlCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	body, err := s.play.Download(dlCtx, src.PackageName, rel.VersionCode, uni.DownloadID)
	if err != nil {
		return nil, &published, err
	}
	defer body.Close()
	app, err := s.catalog.RegisterExpecting(dlCtx, body, s.cfg.MaxUploadBytes, store.AppSourcePlay, src.Label,
		&catalog.Expectation{PackageName: src.PackageName, VersionCode: rel.VersionCode, SignerSHA256: uni.SignerSHA256})
	if err != nil {
		return nil, &published, fmt.Errorf("version %d: %w", rel.VersionCode, err)
	}
	return app, &published, nil
}

// announceNewBuild bumps and notifies every child that has the package.
func (s *Server) announceNewBuild(ctx context.Context, app *store.App) {
	children, err := s.store.ChildrenDeclaring(ctx, app.PackageName)
	if err != nil {
		s.log.Error("play: could not find who has the package", "package", app.PackageName, "error", err)
		return
	}
	detail := map[string]any{"package_name": app.PackageName, "version_code": app.VersionCode, "sha256": app.SHA256}
	if err := s.store.Audit(ctx, store.ActorSystem, "", "PLAY_BUILD_FETCHED", "app", app.ID.String(), detail); err != nil {
		s.log.Error("play: audit", "error", err)
	}
	for _, id := range children {
		if _, err := s.store.BumpPolicyVersion(ctx, id); err != nil {
			s.log.Error("play: policy bump failed", "child", id, "error", err)
		}
		s.publishChild(ctx, id, "policy")
	}
}

// publishChild is notifyChild for a caller with no request: the sync loop.
func (s *Server) publishChild(ctx context.Context, childID uuid.UUID, kind string) {
	ids, err := s.store.DeviceIDsForChild(ctx, childID)
	if err != nil {
		s.log.Error("could not fan out a change", "child", childID, "error", err)
		return
	}
	for _, id := range ids {
		s.hub.PublishDevice(id, Event{Type: kind})
	}
	s.hub.PublishParents(Event{Type: kind, ChildID: childID.String()})
}

// RunPlaySync checks Google Play once at start and then on every interval until ctx ends.
func (s *Server) RunPlaySync(ctx context.Context) {
	if s.play == nil {
		return
	}
	t := time.NewTicker(s.cfg.PlaySyncInterval)
	defer t.Stop()
	for {
		res := s.SyncPlay(ctx)
		if res.Checked > 0 {
			s.log.Info("play: checked", "packages", res.Checked, "fetched", len(res.Registered), "errors", len(res.Errors))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
